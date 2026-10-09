package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// keyCredentialsExecutor answers every Claude request and records which
// credential served it.
type keyCredentialsExecutor struct {
	mu      sync.Mutex
	authIDs []string
}

func (e *keyCredentialsExecutor) Identifier() string { return "claude" }

func (e *keyCredentialsExecutor) Execute(_ context.Context, a *auth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.authIDs = append(e.authIDs, a.ID)
	e.mu.Unlock()
	payload := `{"id":"msg_1","type":"message","role":"assistant","model":"` + req.Model + `","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	return coreexecutor.Response{Payload: []byte(payload)}, nil
}

func (e *keyCredentialsExecutor) ExecuteStream(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, &auth.Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *keyCredentialsExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}

func (e *keyCredentialsExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &auth.Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *keyCredentialsExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, &auth.Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *keyCredentialsExecutor) take() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	authIDs := e.authIDs
	e.authIDs = nil
	return authIDs
}

func TestKeyCredentialsRestrictCredentialSelectionThroughHTTP(t *testing.T) {
	const (
		model      = "claude-key-credentials-model"
		firstAuth  = "aaa-claude-first.json"
		scopedAuth = "claude-scoped.json"
	)
	cfgWith := func(entries ...proxyconfig.ClientKeyCredentials) *proxyconfig.Config {
		return &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{
			APIKeys:        []string{"main-key", "limited-key"},
			KeyCredentials: entries,
		}}
	}
	server := newTestServerWithConfig(t, cfgWith(proxyconfig.ClientKeyCredentials{Key: "limited-key", Credentials: []string{"claude-scoped*"}}))
	server.UpdateClients(server.cfg)
	executor := &keyCredentialsExecutor{}
	manager := server.handlers.AuthManager
	manager.SetSelector(&auth.FillFirstSelector{})
	manager.RegisterExecutor(executor)
	for _, id := range []string{firstAuth, scopedAuth} {
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := manager.Register(context.Background(), &auth.Auth{ID: id, Provider: "claude", Status: auth.StatusActive}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
	}
	send := func(t *testing.T, key string) (int, []string) {
		t.Helper()
		body := `{"model":"` + model + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, request)
		return recorder.Code, executor.take()
	}
	expect := func(t *testing.T, key string, wantStatus int, wantAuthIDs ...string) {
		t.Helper()
		status, authIDs := send(t, key)
		if status != wantStatus || strings.Join(authIDs, ",") != strings.Join(wantAuthIDs, ",") {
			t.Fatalf("%s: status=%d attempts=%v, want status=%d attempts=%v", key, status, authIDs, wantStatus, wantAuthIDs)
		}
	}

	expect(t, "limited-key", http.StatusOK, scopedAuth)
	expect(t, "main-key", http.StatusOK, firstAuth)

	server.UpdateClients(cfgWith(proxyconfig.ClientKeyCredentials{Key: "limited-key", Credentials: []string{"no-such-credential-*"}}))
	if status, authIDs := send(t, "limited-key"); status == http.StatusOK || len(authIDs) != 0 {
		t.Fatalf("after reload narrowing the scope: status=%d attempts=%v, want failure without upstream attempts", status, authIDs)
	}
	expect(t, "main-key", http.StatusOK, firstAuth)

	server.UpdateClients(cfgWith(proxyconfig.ClientKeyCredentials{Key: "unknown-key", Credentials: []string{"claude-scoped*"}}))
	expect(t, "limited-key", http.StatusOK, firstAuth)
}
