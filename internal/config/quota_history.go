package config

import (
	"strings"
	"time"
)

const (
	// DefaultQuotaHistoryInterval is how often quota history polls when no interval is configured.
	DefaultQuotaHistoryInterval = 15 * time.Minute
	// MinQuotaHistoryInterval bounds how often quota history may poll the upstream usage endpoints.
	MinQuotaHistoryInterval = time.Minute
)

// QuotaHistoryConfig controls the background recorder of Claude and Codex quota percentages.
type QuotaHistoryConfig struct {
	// Enabled turns on periodic polling of the provider usage endpoints (default: false).
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Interval is the polling period as a Go duration (default: 15m, minimum: 1m).
	Interval string `yaml:"interval,omitempty" json:"interval,omitempty"`
}

// IntervalDuration returns the effective polling period. An empty or invalid value
// falls back to the default, and values below the minimum are raised to it.
func (c QuotaHistoryConfig) IntervalDuration() time.Duration {
	parsed, errParse := time.ParseDuration(strings.TrimSpace(c.Interval))
	if errParse != nil || parsed <= 0 {
		return DefaultQuotaHistoryInterval
	}
	if parsed < MinQuotaHistoryInterval {
		return MinQuotaHistoryInterval
	}
	return parsed
}
