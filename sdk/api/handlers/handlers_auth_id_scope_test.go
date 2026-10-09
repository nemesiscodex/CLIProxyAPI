package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestGetContextWithCancelCarriesAuthIDScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	scope, _ := coreauth.NewAuthIDScope([]string{"codex-*.json"})
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginCtx.Request = request.WithContext(coreauth.WithAuthIDScope(request.Context(), scope))

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, context.Background())
	defer cancel()
	if got := coreauth.AuthIDScopeFromContext(ctx); got != scope {
		t.Fatalf("AuthIDScopeFromContext() = %p, want %p", got, scope)
	}
}

func TestPluginExecutorRouteRefusedForCredentialScopedKey(t *testing.T) {
	const model = "scoped-plugin-executor-model"
	host := &handlerDirectExecutorRouteHost{}
	host.hasRouters = true
	host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: "executor-plugin"}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)
	scope, _ := coreauth.NewAuthIDScope([]string{"codex-*.json"})
	ctx := coreauth.WithAuthIDScope(context.Background(), scope)
	body := []byte(fmt.Sprintf(`{"model":%q}`, model))

	if _, _, errMsg := handler.ExecuteWithAuthManager(ctx, "claude", model, body, ""); errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("ExecuteWithAuthManager() error = %+v, want 403", errMsg)
	}
	if _, _, errMsg := handler.ExecuteCountWithAuthManager(ctx, "claude", model, body, ""); errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("ExecuteCountWithAuthManager() error = %+v, want 403", errMsg)
	}
	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai", model, body, "")
	if dataChan != nil {
		for range dataChan {
		}
	}
	if errMsg := <-errChan; errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("ExecuteStreamWithAuthManager() error = %+v, want 403", errMsg)
	}
	if host.lastPluginID != "" {
		t.Fatalf("executor plugin %q ran for a credential-scoped key", host.lastPluginID)
	}
}
