package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotahistory"
)

func TestQuotaHistoryRouteServesRecordedSamples(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name    string
		enabled bool
		want    string
	}{
		{
			name:    "enabled",
			enabled: true,
			want:    `{"interval_seconds":300,"accounts":[{"name":"claude-a.json","provider":"claude","windows":{"five-hour":[[1761700000000,88]],"seven-day":[[1761700000000,61]]}}]}`,
		},
		{
			name:    "disabled",
			enabled: false,
			want:    `{"interval_seconds":300,"accounts":[]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			raw := fmt.Sprintf("port: 8317\nmanagement: {secret-key: test-password}\ncredentials:\n  quota-history:\n    enabled: %t\n    interval: 5m\n", tc.enabled)
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			store := quotahistory.NewStore("", func() time.Time { return time.UnixMilli(1761700000000) })
			store.Record("claude-a.json", "claude", map[string]float64{"five-hour": 88, "seven-day": 61})
			h := management.NewHandler(cfg, path, nil)
			h.SetLocalPassword("test-password")
			s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
			s.SetQuotaHistory(store)
			s.managementRoutesEnabled.Store(true)
			s.registerManagementRoutes()

			req := httptest.NewRequest(http.MethodGet, "/v8/management/credentials/quota-history", nil)
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("Authorization", "Bearer test-password")
			recorder := httptest.NewRecorder()
			s.engine.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
			}
			if got := strings.TrimSpace(recorder.Body.String()); got != tc.want {
				t.Fatalf("body = %s\nwant   %s", got, tc.want)
			}
		})
	}
}

func TestQuotaHistoryRouteRequiresManagementKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\nmanagement: {secret-key: test-password}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := management.NewHandler(cfg, path, nil)
	h.SetLocalPassword("test-password")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()

	req := httptest.NewRequest(http.MethodGet, "/v8/management/credentials/quota-history", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	recorder := httptest.NewRecorder()
	s.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}
