package auth

import (
	"context"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAuthIDScopeAllows(t *testing.T) {
	t.Parallel()

	scope, errs := NewAuthIDScope([]string{"claude-7cb6bfeb-*.json", "codex-*.json", "[bad", "claude:apikey:*"})
	if len(errs) != 1 {
		t.Fatalf("NewAuthIDScope() errors = %v, want one malformed pattern", errs)
	}
	tests := []struct {
		authID string
		want   bool
	}{
		{authID: "claude-7cb6bfeb-user@example.com.json", want: true},
		{authID: "codex-user@example.com-plus.json", want: true},
		{authID: "claude:apikey:0123456789ab", want: true},
		{authID: "claude-other-user@example.com.json", want: false},
		{authID: "team/codex-user.json", want: false},
		{authID: "[bad", want: false},
	}
	for _, tt := range tests {
		if got := scope.Allows(tt.authID); got != tt.want {
			t.Errorf("Allows(%q) = %v, want %v", tt.authID, got, tt.want)
		}
	}
	if empty, _ := NewAuthIDScope(nil); empty.Allows("anything.json") {
		t.Error("a scope without patterns must allow nothing")
	}
	var unrestricted *AuthIDScope
	if !unrestricted.Allows("anything.json") {
		t.Error("a nil scope must allow every credential")
	}
}

// scopeRecordingExecutor records the credential of every upstream attempt.
type scopeRecordingExecutor struct {
	mu       sync.Mutex
	authIDs  []string
	failWith error
}

func (e *scopeRecordingExecutor) Identifier() string { return "claude" }

func (e *scopeRecordingExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.authIDs = append(e.authIDs, auth.ID)
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, e.failWith
}

func (e *scopeRecordingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *scopeRecordingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *scopeRecordingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *scopeRecordingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *scopeRecordingExecutor) attempts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

// lowestIDSelector is a non-built-in selector, so the manager takes the
// legacy selector path instead of the scheduler fast path.
type lowestIDSelector struct{}

func (lowestIDSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	var selected *Auth
	for _, auth := range auths {
		if auth != nil && (selected == nil || auth.ID < selected.ID) {
			selected = auth
		}
	}
	return selected, nil
}

func TestManagerExecuteRespectsAuthIDScope(t *testing.T) {
	const (
		allowedA = "claude-allowed-a.json"
		allowedB = "claude-allowed-b.json"
		other    = "aaa-claude-other.json"
	)
	selectors := map[string]func() Selector{
		"scheduler fast path": func() Selector { return &FillFirstSelector{} },
		"legacy selector":     func() Selector { return lowestIDSelector{} },
	}
	for selectorName, newSelector := range selectors {
		t.Run(selectorName, func(t *testing.T) {
			model := "claude-scope-" + selectorName
			newManager := func(t *testing.T, executor *scopeRecordingExecutor, cooled ...string) *Manager {
				t.Helper()
				manager := NewManager(nil, newSelector(), nil)
				manager.RegisterExecutor(executor)
				for _, id := range []string{allowedA, allowedB, other} {
					auth := &Auth{ID: id, Provider: "claude", Status: StatusActive}
					for _, cooledID := range cooled {
						if cooledID == id {
							next := time.Now().Add(time.Hour)
							auth.ModelStates = map[string]*ModelState{model: {
								Status:         StatusError,
								Unavailable:    true,
								NextRetryAfter: next,
								Quota:          QuotaState{Exceeded: true, NextRecoverAt: next},
							}}
						}
					}
					registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
					if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
						t.Fatalf("Register(%s) error = %v", id, errRegister)
					}
				}
				t.Cleanup(func() {
					for _, id := range []string{allowedA, allowedB, other} {
						registry.GetGlobalRegistry().UnregisterClient(id)
					}
				})
				return manager
			}
			scope, _ := NewAuthIDScope([]string{"claude-allowed-*.json"})
			restricted := WithAuthIDScope(context.Background(), scope)
			execute := func(manager *Manager, ctx context.Context, opts cliproxyexecutor.Options) error {
				_, errExecute := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts)
				return errExecute
			}

			t.Run("restricted key uses only matching credentials", func(t *testing.T) {
				executor := &scopeRecordingExecutor{}
				if errExecute := execute(newManager(t, executor), restricted, cliproxyexecutor.Options{}); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				if got := executor.attempts(); !reflect.DeepEqual(got, []string{allowedA}) {
					t.Fatalf("attempts = %v, want [%s]", got, allowedA)
				}
			})

			t.Run("unrestricted key is unaffected", func(t *testing.T) {
				executor := &scopeRecordingExecutor{}
				if errExecute := execute(newManager(t, executor), context.Background(), cliproxyexecutor.Options{}); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				if got := executor.attempts(); !reflect.DeepEqual(got, []string{other}) {
					t.Fatalf("attempts = %v, want [%s]", got, other)
				}
			})

			t.Run("retries stay inside the scope", func(t *testing.T) {
				executor := &scopeRecordingExecutor{failWith: &Error{HTTPStatus: http.StatusInternalServerError, Message: "upstream failed"}}
				if errExecute := execute(newManager(t, executor), restricted, cliproxyexecutor.Options{}); errExecute == nil {
					t.Fatal("Execute() error = nil, want the upstream failure")
				}
				got := executor.attempts()
				sort.Strings(got)
				if !reflect.DeepEqual(got, []string{allowedA, allowedB}) {
					t.Fatalf("attempts = %v, want only %s and %s", got, allowedA, allowedB)
				}
			})

			t.Run("no fallback when matching credentials are cooling down", func(t *testing.T) {
				executor := &scopeRecordingExecutor{}
				manager := newManager(t, executor, allowedA, allowedB)
				if errExecute := execute(manager, restricted, cliproxyexecutor.Options{}); errExecute == nil {
					t.Fatal("Execute() error = nil, want cooldown error")
				}
				if got := executor.attempts(); len(got) != 0 {
					t.Fatalf("attempts = %v, want none", got)
				}
				if errExecute := execute(manager, context.Background(), cliproxyexecutor.Options{}); errExecute != nil {
					t.Fatalf("unrestricted Execute() error = %v", errExecute)
				}
				if got := executor.attempts(); !reflect.DeepEqual(got, []string{other}) {
					t.Fatalf("unrestricted attempts = %v, want [%s]", got, other)
				}
			})

			t.Run("pinned credential outside the scope is refused", func(t *testing.T) {
				executor := &scopeRecordingExecutor{}
				opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: other}}
				if errExecute := execute(newManager(t, executor), restricted, opts); errExecute == nil {
					t.Fatal("Execute() error = nil, want no auth available")
				}
				if got := executor.attempts(); len(got) != 0 {
					t.Fatalf("attempts = %v, want none", got)
				}
			})
		})
	}
}

// scopedCreditsExecutor fails every non-credits stream with 429 and records
// which credential each credits attempt used.
type scopedCreditsExecutor struct {
	antigravityCreditsFallbackExecutor
	creditsAuthIDs []string
}

func (e *scopedCreditsExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if AntigravityCreditsRequested(ctx) {
		e.creditsAuthIDs = append(e.creditsAuthIDs, auth.ID)
	}
	return e.antigravityCreditsFallbackExecutor.ExecuteStream(ctx, auth, req, opts)
}

func TestAntigravityCreditsFallbackRespectsAuthIDScope(t *testing.T) {
	const (
		model   = "claude-opus-4-6-thinking"
		other   = "ag-a-other.json"
		allowed = "ag-b-allowed.json"
	)
	executor := &scopedCreditsExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true}})
	manager.RegisterExecutor(executor)
	for _, id := range []string{other, allowed} {
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "antigravity"}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
	}
	scope, _ := NewAuthIDScope([]string{"ag-b-*"})
	streamResult, errExecute := manager.ExecuteStream(WithAuthIDScope(context.Background(), scope), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for range streamResult.Chunks {
	}
	if !reflect.DeepEqual(executor.creditsAuthIDs, []string{allowed}) {
		t.Fatalf("credits attempts = %v, want [%s]", executor.creditsAuthIDs, allowed)
	}
}
