package quotahistory

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fetchCall struct {
	name   string
	url    string
	header map[string]string
}

// fakeUpstream answers per credential file name and records what each call sent.
type fakeUpstream struct {
	mu        sync.Mutex
	responses map[string]fakeResponse
	calls     []fetchCall
	inFlight  int
	maxFlight int
}

type fakeResponse struct {
	status int
	body   string
	err    error
}

func (u *fakeUpstream) fetch(_ context.Context, auth *coreauth.Auth, url string, header map[string]string) (int, []byte, error) {
	u.mu.Lock()
	u.inFlight++
	if u.inFlight > u.maxFlight {
		u.maxFlight = u.inFlight
	}
	u.calls = append(u.calls, fetchCall{name: auth.FileName, url: url, header: header})
	response := u.responses[auth.FileName]
	u.inFlight--
	u.mu.Unlock()
	return response.status, []byte(response.body), response.err
}

func codexIDToken(accountID string) string {
	claims := `{"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `"}}`
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
}

func oauthAuth(file, provider string) *coreauth.Auth {
	return &coreauth.Auth{ID: file, FileName: file, Provider: provider, Metadata: map[string]any{"access_token": "secret"}}
}

func windowsByName(accounts []Account) map[string]map[string]float64 {
	out := make(map[string]map[string]float64, len(accounts))
	for _, account := range accounts {
		latest := make(map[string]float64, len(account.Windows))
		for window, samples := range account.Windows {
			latest[window] = samples[len(samples)-1].Remaining
		}
		out[account.Name] = latest
	}
	return out
}

func TestPollOnceRecordsEachEnabledClaudeAndCodexCredential(t *testing.T) {
	store, _ := newTestStore(t)
	codexAuth := oauthAuth("codex-b.json", "codex")
	codexAuth.Metadata["id_token"] = codexIDToken("acct-123")
	disabled := oauthAuth("claude-off.json", "claude")
	disabled.Disabled = true
	auths := []*coreauth.Auth{
		oauthAuth("claude-a.json", "claude"),
		codexAuth,
		oauthAuth("claude-expired.json", "claude"),
		oauthAuth("claude-offline.json", "claude"),
		disabled,
		oauthAuth("gemini.json", "gemini"),
	}
	upstream := &fakeUpstream{responses: map[string]fakeResponse{
		"claude-a.json":       {status: 200, body: `{"five_hour":{"utilization":12},"seven_day":{"utilization":39}}`},
		"codex-b.json":        {status: 200, body: `{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":40,"limit_window_seconds":604800}}}`},
		"claude-expired.json": {status: 401, body: `{"error":"unauthorized"}`},
		"claude-offline.json": {err: errors.New("dial tcp: connection refused")},
	}}
	poller := NewPoller(context.Background(), store, func() []*coreauth.Auth { return auths }, upstream.fetch)

	poller.PollOnce(context.Background())

	want := map[string]map[string]float64{
		"claude-a.json": {"five-hour": 88, "seven-day": 61},
		"codex-b.json":  {"five-hour": 90, "weekly": 60},
	}
	if got := windowsByName(store.Accounts()); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded = %v, want %v", got, want)
	}
	if upstream.maxFlight != 1 {
		t.Fatalf("polled %d credentials at once, want one at a time", upstream.maxFlight)
	}
	polled := make(map[string]fetchCall, len(upstream.calls))
	for _, call := range upstream.calls {
		polled[call.name] = call
	}
	if _, ok := polled["claude-off.json"]; ok {
		t.Fatal("disabled credential was polled")
	}
	if _, ok := polled["gemini.json"]; ok {
		t.Fatal("unsupported provider was polled")
	}
	claude := polled["claude-a.json"]
	if claude.url != "https://api.anthropic.com/api/oauth/usage" || claude.header["Authorization"] != "Bearer $TOKEN$" || claude.header["anthropic-beta"] != "oauth-2025-04-20" {
		t.Fatalf("claude request = %+v", claude)
	}
	codex := polled["codex-b.json"]
	if codex.url != "https://chatgpt.com/backend-api/wham/usage" || codex.header["Authorization"] != "Bearer $TOKEN$" || codex.header["Chatgpt-Account-Id"] != "acct-123" {
		t.Fatalf("codex request = %+v", codex)
	}

	reloaded := NewStore(store.Path(), nil)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if got := windowsByName(reloaded.Accounts()); !reflect.DeepEqual(got, want) {
		t.Fatalf("saved = %v, want %v", got, want)
	}
}

func TestPollOnceDropsHistoryOfDisabledAndRemovedCredentials(t *testing.T) {
	store, _ := newTestStore(t)
	store.Record("claude-a.json", "claude", map[string]float64{"five-hour": 50})
	store.Record("claude-gone.json", "claude", map[string]float64{"five-hour": 50})
	store.Record("codex-paused.json", "codex", map[string]float64{"weekly": 50})
	paused := oauthAuth("codex-paused.json", "codex")
	paused.Disabled = true
	auths := []*coreauth.Auth{oauthAuth("claude-a.json", "claude"), paused}
	upstream := &fakeUpstream{responses: map[string]fakeResponse{"claude-a.json": {status: 503}}}
	poller := NewPoller(context.Background(), store, func() []*coreauth.Auth { return auths }, upstream.fetch)

	poller.PollOnce(context.Background())

	want := map[string]map[string]float64{"claude-a.json": {"five-hour": 50}}
	if got := windowsByName(store.Accounts()); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %v, want %v", got, want)
	}
}

func TestPollerPollsAfterStartAndStopWaitsForIt(t *testing.T) {
	store, _ := newTestStore(t)
	polled := make(chan struct{}, 1)
	fetch := func(context.Context, *coreauth.Auth, string, map[string]string) (int, []byte, error) {
		select {
		case polled <- struct{}{}:
		default:
		}
		return 200, []byte(`{"five_hour":{"utilization":1}}`), nil
	}
	auths := []*coreauth.Auth{oauthAuth("claude-a.json", "claude")}
	poller := NewPoller(context.Background(), store, func() []*coreauth.Auth { return auths }, fetch)
	poller.firstDelay = 0

	poller.Apply(true, time.Hour)
	<-polled
	poller.Stop()

	if got := store.Accounts(); len(got) != 1 || len(got[0].Windows["five-hour"]) != 1 {
		t.Fatalf("history after one poll = %+v", got)
	}
}

func TestPollerDoesNotStartAfterParentIsCancelled(t *testing.T) {
	store, _ := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	fetch := func(context.Context, *coreauth.Auth, string, map[string]string) (int, []byte, error) {
		called = true
		return 200, nil, nil
	}
	poller := NewPoller(ctx, store, func() []*coreauth.Auth { return []*coreauth.Auth{oauthAuth("a.json", "claude")} }, fetch)
	poller.firstDelay = 0
	poller.Apply(true, time.Minute)
	poller.Stop()
	if called {
		t.Fatal("poller ran after service shutdown")
	}
}
