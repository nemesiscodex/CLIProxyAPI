package auth

import (
	"strconv"
	"testing"
	"time"
)

func unixSignal(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

func TestQuotaWindowsFor(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	observed := now.Add(-10 * time.Minute)

	tests := []struct {
		name     string
		provider string
		quota    QuotaState
		want     []quotaWindow
	}{
		{
			name:     "claude 5h and 7d longest first",
			provider: "claude",
			quota: QuotaState{Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Reset":       unixSignal(now.Add(2 * time.Hour)),
				"Anthropic-Ratelimit-Unified-5h-Utilization": "0.09",
				"Anthropic-Ratelimit-Unified-7d-Reset":       unixSignal(now.Add(3 * 24 * time.Hour)),
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.52",
			}},
			want: []quotaWindow{
				{length: 7 * 24 * time.Hour, resetAt: now.Add(3 * 24 * time.Hour)},
				{length: 5 * time.Hour, resetAt: now.Add(2 * time.Hour)},
			},
		},
		{
			name:     "provider name is case insensitive",
			provider: " Claude ",
			quota: QuotaState{Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Reset": unixSignal(now.Add(time.Hour)),
			}},
			want: []quotaWindow{{length: 5 * time.Hour, resetAt: now.Add(time.Hour)}},
		},
		{
			name:     "codex reset-at with window minutes",
			provider: "codex",
			quota: QuotaState{ObservedAt: observed, Signals: map[string]string{
				"X-Codex-Primary-Reset-At":            unixSignal(now.Add(time.Hour)),
				"X-Codex-Primary-Reset-After-Seconds": "1",
				"X-Codex-Primary-Window-Minutes":      "300",
				"X-Codex-Secondary-Reset-At":          unixSignal(now.Add(48 * time.Hour)),
				"X-Codex-Secondary-Window-Minutes":    "10080",
			}},
			want: []quotaWindow{
				{length: 10080 * time.Minute, resetAt: now.Add(48 * time.Hour)},
				{length: 300 * time.Minute, resetAt: now.Add(time.Hour)},
			},
		},
		{
			name:     "codex reset-after-seconds is relative to observation",
			provider: "codex",
			quota: QuotaState{ObservedAt: observed, Signals: map[string]string{
				"X-Codex-Primary-Reset-After-Seconds":   "3600",
				"X-Codex-Secondary-Reset-After-Seconds": "86400",
			}},
			want: []quotaWindow{
				{length: 7 * 24 * time.Hour, resetAt: observed.Add(24 * time.Hour)},
				{length: 5 * time.Hour, resetAt: observed.Add(time.Hour)},
			},
		},
		{
			name:     "codex reset-after-seconds needs an observation time",
			provider: "codex",
			quota: QuotaState{Signals: map[string]string{
				"X-Codex-Primary-Reset-After-Seconds": "3600",
			}},
			want: nil,
		},
		{
			name:     "codex additional limits are ignored",
			provider: "codex",
			quota: QuotaState{ObservedAt: observed, Signals: map[string]string{
				"X-Codex-Additional-Bengalfox-Primary-Reset-At": unixSignal(now.Add(time.Hour)),
				"X-Codex-Code-Review-Primary-Reset-At":          unixSignal(now.Add(time.Hour)),
			}},
			want: nil,
		},
		{
			name:     "garbage values are skipped and fall back",
			provider: "codex",
			quota: QuotaState{ObservedAt: observed, Signals: map[string]string{
				"X-Codex-Primary-Reset-At":              "soon",
				"X-Codex-Primary-Reset-After-Seconds":   "1200",
				"X-Codex-Primary-Window-Minutes":        "-5",
				"X-Codex-Secondary-Reset-At":            "",
				"X-Codex-Secondary-Reset-After-Seconds": "1.5",
			}},
			want: []quotaWindow{{length: 5 * time.Hour, resetAt: observed.Add(20 * time.Minute)}},
		},
		{
			name:     "stale reset rolls forward by whole windows",
			provider: "claude",
			quota: QuotaState{Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Reset": unixSignal(now.Add(-11 * time.Hour)),
				"Anthropic-Ratelimit-Unified-7d-Reset": unixSignal(now),
			}},
			want: []quotaWindow{
				{length: 7 * 24 * time.Hour, resetAt: now.Add(7 * 24 * time.Hour)},
				{length: 5 * time.Hour, resetAt: now.Add(4 * time.Hour)},
			},
		},
		{
			name:     "provider without signals support",
			provider: "gemini",
			quota: QuotaState{Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Reset": unixSignal(now.Add(time.Hour)),
			}},
			want: nil,
		},
		{
			name:     "no signals",
			provider: "claude",
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := quotaWindowsFor(tt.provider, tt.quota, now)
			if len(got) != len(tt.want) {
				t.Fatalf("quotaWindowsFor() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i].length != tt.want[i].length || !got[i].resetAt.Equal(tt.want[i].resetAt) {
					t.Fatalf("quotaWindowsFor()[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func claudeQuotaAuth(id string, fiveHourReset, sevenDayReset time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Quota: QuotaState{Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Reset": unixSignal(fiveHourReset),
			"Anthropic-Ratelimit-Unified-7d-Reset": unixSignal(sevenDayReset),
		}},
	}
}

func TestSoonestResetLess(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	weeklySoon := claudeQuotaAuth("b", now.Add(4*time.Hour), now.Add(24*time.Hour))
	weeklyLate := claudeQuotaAuth("a", now.Add(time.Hour), now.Add(48*time.Hour))
	weeklySoonTwin := claudeQuotaAuth("c", now.Add(4*time.Hour), now.Add(24*time.Hour))
	weeklySoonEarlierFiveHour := claudeQuotaAuth("d", now.Add(2*time.Hour), now.Add(24*time.Hour))
	noSignals := &Auth{ID: "0", Provider: "claude"}

	tests := []struct {
		name string
		a, b *Auth
		want bool
	}{
		{name: "earlier weekly reset wins over earlier 5h reset", a: weeklySoon, b: weeklyLate, want: true},
		{name: "later weekly reset loses", a: weeklyLate, b: weeklySoon, want: false},
		{name: "equal weekly falls through to 5h", a: weeklySoonEarlierFiveHour, b: weeklySoon, want: true},
		{name: "equal windows tie break by id", a: weeklySoon, b: weeklySoonTwin, want: true},
		{name: "equal windows tie break by id reversed", a: weeklySoonTwin, b: weeklySoon, want: false},
		{name: "auth with windows beats auth without", a: weeklyLate, b: noSignals, want: true},
		{name: "auth without windows loses despite lower id", a: noSignals, b: weeklyLate, want: false},
		{name: "nil sorts last", a: weeklyLate, b: nil, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := soonestResetLess(tt.a, tt.b, now); got != tt.want {
				t.Fatalf("soonestResetLess(%s, %s) = %v, want %v", idOf(tt.a), idOf(tt.b), got, tt.want)
			}
		})
	}
}

func idOf(auth *Auth) string {
	if auth == nil {
		return "<nil>"
	}
	return auth.ID
}
