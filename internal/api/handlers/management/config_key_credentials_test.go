package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigWritesPreserveKeyCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "config-version: 8\naccess:\n  api-keys: [main-key, limited-key]\n  key-credentials:\n    - key: limited-key\n      credentials: [codex-*.json]\nrouting:\n  retry:\n    request-retry: 3\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PATCH("/v8/management/config", h.ConfigV8)
	router.PUT("/v0/management/request-retry", h.PutRequestRetry)
	router.PUT("/v0/management/api-keys", h.PutAPIKeys)
	want := []config.ClientKeyCredentials{{Key: "limited-key", Credentials: []string{"codex-*.json"}}}

	for _, write := range []struct{ method, url, body string }{
		{http.MethodPatch, "/v8/management/config", `{"routing":{"retry":{"request-retry":1}},"access":{"api-keys":["main-key","limited-key","new-key"]}}`},
		{http.MethodPut, "/v0/management/request-retry", `{"value":2}`},
		{http.MethodPut, "/v0/management/api-keys", `["main-key","limited-key"]`},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(write.method, write.url, strings.NewReader(write.body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s: status=%d body=%s", write.method, write.url, recorder.Code, recorder.Body.String())
		}
		saved, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		reloaded, errLoad := config.LoadConfig(path)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		if !reflect.DeepEqual(reloaded.KeyCredentials, want) || !strings.Contains(string(saved), "key-credentials:") {
			t.Fatalf("%s %s dropped access.key-credentials:\n%s", write.method, write.url, saved)
		}
	}
}
