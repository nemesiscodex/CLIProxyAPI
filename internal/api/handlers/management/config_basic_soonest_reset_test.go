package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPutRoutingStrategyAcceptsSoonestReset(t *testing.T) {
	for _, input := range []string{"soonest-reset", "SoonestReset", "sr"} {
		t.Run(input, func(t *testing.T) {
			h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/routing/strategy", strings.NewReader(`{"value":"`+input+`"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			h.PutRoutingStrategy(ctx)
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if h.cfg.Routing.Strategy != "soonest-reset" {
				t.Fatalf("Routing.Strategy = %q, want soonest-reset", h.cfg.Routing.Strategy)
			}
		})
	}
}
