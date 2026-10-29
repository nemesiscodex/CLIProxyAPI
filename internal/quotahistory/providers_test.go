package quotahistory

import (
	"reflect"
	"testing"
)

func parseUsage(t *testing.T, provider, body string) map[string]float64 {
	t.Helper()
	remaining, err := usageSources[provider].parse([]byte(body))
	if err != nil {
		t.Fatalf("parse %s usage: %v", provider, err)
	}
	return remaining
}

func TestClaudeUsageRecordsAccountWideWindowsAsRemainingPercent(t *testing.T) {
	got := parseUsage(t, "claude", `{
		"five_hour": {"utilization": 12, "resets_at": "2026-10-29T15:00:00Z"},
		"seven_day": {"utilization": "39.5", "resets_at": "2026-11-02T00:00:00Z"},
		"seven_day_opus": {"utilization": 80},
		"seven_day_sonnet": {"utilization": 5},
		"iguana_necktie": {"utilization": 41},
		"extra_usage": {"is_enabled": false}
	}`)
	want := map[string]float64{"five-hour": 88, "seven-day": 60.5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windows = %v, want %v", got, want)
	}
}

func TestClaudeUsageClampsAndSkipsUnreadableWindows(t *testing.T) {
	got := parseUsage(t, "claude", `{"five_hour": {"utilization": 130}, "seven_day": {"utilization": null}}`)
	want := map[string]float64{"five-hour": 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windows = %v, want %v", got, want)
	}
}

func TestCodexUsageClassifiesWindowsByDuration(t *testing.T) {
	got := parseUsage(t, "codex", `{
		"rate_limit": {
			"allowed": true,
			"limit_reached": false,
			"primary_window": {"used_percent": 40, "limit_window_seconds": 604800, "reset_after_seconds": 3600},
			"secondary_window": {"used_percent": 10, "limit_window_seconds": 18000, "reset_after_seconds": 600}
		},
		"code_review_rate_limit": {
			"primary_window": {"used_percent": 99, "limit_window_seconds": 18000}
		},
		"additional_rate_limits": [
			{"limit_name": "GPT-5 Pro", "rate_limit": {"primary_window": {"used_percent": 77, "limit_window_seconds": 604800}}}
		]
	}`)
	want := map[string]float64{"five-hour": 90, "weekly": 60}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windows = %v, want %v", got, want)
	}
}

func TestCodexUsageNamesThirtyDayWindowMonthly(t *testing.T) {
	got := parseUsage(t, "codex", `{
		"rate_limit": {
			"primary_window": {"used_percent": 25, "limit_window_seconds": 2592000}
		}
	}`)
	want := map[string]float64{"monthly": 75}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windows = %v, want %v", got, want)
	}
}

func TestCodexUsageFallsBackToPrimarySecondaryOrderWithoutDurations(t *testing.T) {
	got := parseUsage(t, "codex", `{
		"rateLimit": {
			"primaryWindow": {"usedPercent": 30},
			"secondaryWindow": {"usedPercent": 55}
		}
	}`)
	want := map[string]float64{"five-hour": 70, "weekly": 45}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windows = %v, want %v", got, want)
	}
}

func TestCodexUsageCountsReachedLimitWithKnownResetAsFullyUsed(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want map[string]float64
	}{
		{
			name: "limit_reached",
			body: `{"rate_limit": {"limit_reached": true,
				"primary_window": {"limit_window_seconds": 18000, "reset_after_seconds": 1200},
				"secondary_window": {"limit_window_seconds": 604800}}}`,
			want: map[string]float64{"five-hour": 0},
		},
		{
			name: "allowed false",
			body: `{"rate_limit": {"allowed": false,
				"primary_window": {"limit_window_seconds": 18000, "reset_at": 1761703600},
				"secondary_window": {"used_percent": 20, "limit_window_seconds": 604800}}}`,
			want: map[string]float64{"five-hour": 0, "weekly": 80},
		},
		{
			name: "not reached",
			body: `{"rate_limit": {"allowed": true, "limit_reached": false,
				"primary_window": {"limit_window_seconds": 18000, "reset_after_seconds": 1200}}}`,
			want: map[string]float64{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseUsage(t, "codex", tc.body); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("windows = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUsageRejectsNonJSONBody(t *testing.T) {
	for provider, source := range usageSources {
		if _, err := source.parse([]byte("<html>login</html>")); err == nil {
			t.Fatalf("%s: expected an error for a non-JSON body", provider)
		}
	}
}
