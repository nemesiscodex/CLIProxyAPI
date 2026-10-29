package cliproxy

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotahistory"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// startQuotaHistory loads saved quota history, serves it through the management API and
// creates the poller. It must run before the server and config watchers start, because
// applyQuotaHistoryConfig reads s.quotaHistory from config reload goroutines.
func (s *Service) startQuotaHistory(ctx context.Context) {
	if s == nil || s.server == nil || s.coreManager == nil {
		return
	}
	store := quotahistory.NewStore(quotahistory.DefaultPath(s.configPath), time.Now)
	if errLoad := store.Load(); errLoad != nil {
		log.WithError(errLoad).Warn("quota history: starting empty")
	}
	if store.Path() == "" {
		log.Warn("quota history: no config path or WRITABLE_PATH; history is kept in memory only")
	}
	s.server.SetQuotaHistory(store)
	s.quotaHistory = quotahistory.NewPoller(ctx, store, s.coreManager.List, s.server.FetchWithCredential)
	s.applyQuotaHistoryConfig(s.currentConfig())
}

// applyQuotaHistoryConfig follows config hot reloads. Home mode keeps credentials
// off this process, so it never polls.
func (s *Service) applyQuotaHistoryConfig(cfg *config.Config) {
	if s == nil || s.quotaHistory == nil || cfg == nil {
		return
	}
	s.quotaHistory.Apply(cfg.QuotaHistory.Enabled && !cfg.Home.Enabled, cfg.QuotaHistory.IntervalDuration())
}

func (s *Service) stopQuotaHistory() {
	if s == nil || s.quotaHistory == nil {
		return
	}
	s.quotaHistory.Stop()
}
