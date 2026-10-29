package api

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotahistory"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// SetQuotaHistory exposes recorded quota history through the management API.
func (s *Server) SetQuotaHistory(store *quotahistory.Store) {
	s.mgmt.SetQuotaHistory(store)
}

// FetchWithCredential sends a GET with the credential's token and proxy settings, the
// way the management api-call tool does. It satisfies quotahistory.FetchFunc.
func (s *Server) FetchWithCredential(ctx context.Context, auth *coreauth.Auth, url string, header map[string]string) (int, []byte, error) {
	return s.mgmt.FetchWithCredential(ctx, auth, url, header)
}
