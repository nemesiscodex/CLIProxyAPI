package management

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotahistory"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type quotaHistoryResponse struct {
	IntervalSeconds int64                  `json:"interval_seconds"`
	Accounts        []quotahistory.Account `json:"accounts"`
}

// SetQuotaHistory attaches the store served by GetQuotaHistory.
func (h *Handler) SetQuotaHistory(store *quotahistory.Store) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.quotaHistory = store
	h.mu.Unlock()
}

// GetQuotaHistory returns the recorded remaining-percent samples per credential and window.
//
// Endpoint:
//
//	GET /v8/management/credentials/quota-history
//
// When quota history is disabled the response still carries the configured interval
// and an empty account list, so clients can tell it apart from a server without the route.
func (h *Handler) GetQuotaHistory(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	store := h.quotaHistory
	h.mu.Unlock()

	var settings config.QuotaHistoryConfig
	if cfg != nil {
		settings = cfg.QuotaHistory
	}
	response := quotaHistoryResponse{
		IntervalSeconds: int64(settings.IntervalDuration() / time.Second),
		Accounts:        []quotahistory.Account{},
	}
	if settings.Enabled && store != nil {
		response.Accounts = store.Accounts()
	}
	c.JSON(http.StatusOK, response)
}

// FetchWithCredential sends a GET to url on behalf of auth. "$TOKEN$" in header values is
// replaced by the credential token. Token resolution and proxy selection are the ones
// APICall uses. The request is bounded only by ctx, not by a timeout.
func (h *Handler) FetchWithCredential(ctx context.Context, auth *coreauth.Auth, url string, header map[string]string) (int, []byte, error) {
	token, errToken := h.resolveTokenForAuth(ctx, auth, "")
	if errToken != nil {
		return 0, nil, errors.New("auth token refresh failed")
	}
	if token == "" {
		return 0, nil, errors.New("auth token not found")
	}
	req, errNewRequest := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if errNewRequest != nil {
		return 0, nil, fmt.Errorf("build request: %w", errNewRequest)
	}
	for key, value := range header {
		req.Header.Set(key, strings.ReplaceAll(value, "$TOKEN$", token))
	}
	httpClient := &http.Client{Transport: h.apiCallTransport(auth, "")}
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return 0, nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return 0, nil, fmt.Errorf("read response: %w", errRead)
	}
	return resp.StatusCode, body, nil
}
