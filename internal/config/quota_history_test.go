package config

import (
	"testing"
	"time"
)

func TestQuotaHistoryIntervalDefaultsAndMinimum(t *testing.T) {
	for _, tc := range []struct {
		interval string
		want     time.Duration
	}{
		{"", 15 * time.Minute},
		{"not-a-duration", 15 * time.Minute},
		{"-5m", 15 * time.Minute},
		{"30s", time.Minute},
		{"1m", time.Minute},
		{"1h", time.Hour},
	} {
		if got := (QuotaHistoryConfig{Interval: tc.interval}).IntervalDuration(); got != tc.want {
			t.Errorf("interval %q = %s, want %s", tc.interval, got, tc.want)
		}
	}
}

func TestQuotaHistoryReadsBothConfigLayouts(t *testing.T) {
	for name, raw := range map[string]string{
		"v8":     "credentials:\n  quota-history:\n    enabled: true\n    interval: 10m\n",
		"legacy": "quota-history:\n  enabled: true\n  interval: 10m\n",
	} {
		cfg, err := ParseConfigBytes([]byte("port: 8317\n" + raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !cfg.QuotaHistory.Enabled || cfg.QuotaHistory.IntervalDuration() != 10*time.Minute {
			t.Fatalf("%s: quota history = %+v", name, cfg.QuotaHistory)
		}
	}
	if err := ValidateV8Config([]byte("config-version: 8\nserver: {port: 8317}\ncredentials:\n  quota-history:\n    enabled: true\n    interval: 10m\n")); err != nil {
		t.Fatalf("v8 layout rejected quota-history: %v", err)
	}
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QuotaHistory.Enabled {
		t.Fatal("quota history must be disabled by default")
	}
}
