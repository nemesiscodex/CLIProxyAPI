package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestFetchWithCredentialSubstitutesTheCredentialToken(t *testing.T) {
	var gotAuthorization, gotBeta string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization, gotBeta = r.Header.Get("Authorization"), r.Header.Get("anthropic-beta")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":3}}`))
	}))
	defer upstream.Close()

	h := NewHandler(&config.Config{}, "", nil)
	auth := &coreauth.Auth{ID: "claude-a.json", Provider: "claude", Metadata: map[string]any{"access_token": "tok-123"}}
	status, body, err := h.FetchWithCredential(context.Background(), auth, upstream.URL, map[string]string{
		"Authorization":  "Bearer $TOKEN$",
		"anthropic-beta": "oauth-2025-04-20",
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusTeapot || string(body) != `{"five_hour":{"utilization":3}}` {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if gotAuthorization != "Bearer tok-123" || gotBeta != "oauth-2025-04-20" {
		t.Fatalf("upstream saw Authorization=%q anthropic-beta=%q", gotAuthorization, gotBeta)
	}
}

func TestFetchWithCredentialFailsWithoutAToken(t *testing.T) {
	h := NewHandler(&config.Config{}, "", nil)
	auth := &coreauth.Auth{ID: "claude-a.json", Provider: "claude"}
	if _, _, err := h.FetchWithCredential(context.Background(), auth, "http://127.0.0.1:1", nil); err == nil {
		t.Fatal("expected an error for a credential without a token")
	}
}
