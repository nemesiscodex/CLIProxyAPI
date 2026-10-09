package auth

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// quotaWindow is one rolling quota window of a credential and the time it next resets.
type quotaWindow struct {
	length  time.Duration
	resetAt time.Time
}

// quotaWindowSpec names the quota signal headers that describe one window.
type quotaWindowSpec struct {
	resetAtKey       string
	resetAfterKey    string
	lengthMinutesKey string
	defaultLength    time.Duration
}

const (
	quotaWindowFiveHours = 5 * time.Hour
	quotaWindowSevenDays = 7 * 24 * time.Hour
)

// quotaWindowSpecs maps a provider to the credential-level windows it reports.
// Codex additional and code-review limits are intentionally excluded because
// they do not gate the credential as a whole.
var quotaWindowSpecs = map[string][]quotaWindowSpec{
	"claude": {
		{resetAtKey: "Anthropic-Ratelimit-Unified-5h-Reset", defaultLength: quotaWindowFiveHours},
		{resetAtKey: "Anthropic-Ratelimit-Unified-7d-Reset", defaultLength: quotaWindowSevenDays},
	},
	"codex": {
		{
			resetAtKey:       "X-Codex-Primary-Reset-At",
			resetAfterKey:    "X-Codex-Primary-Reset-After-Seconds",
			lengthMinutesKey: "X-Codex-Primary-Window-Minutes",
			defaultLength:    quotaWindowFiveHours,
		},
		{
			resetAtKey:       "X-Codex-Secondary-Reset-At",
			resetAfterKey:    "X-Codex-Secondary-Reset-After-Seconds",
			lengthMinutesKey: "X-Codex-Secondary-Window-Minutes",
			defaultLength:    quotaWindowSevenDays,
		},
	},
}

// quotaWindowsFor returns the provider's known quota windows, longest first.
// A reset at or before now is rolled forward by whole window lengths: the
// window has already renewed, and the projection stands until a fresh
// response replaces the snapshot.
func quotaWindowsFor(provider string, quota QuotaState, now time.Time) []quotaWindow {
	specs := quotaWindowSpecs[strings.ToLower(strings.TrimSpace(provider))]
	if len(specs) == 0 || len(quota.Signals) == 0 {
		return nil
	}
	windows := make([]quotaWindow, 0, len(specs))
	for _, spec := range specs {
		length := spec.defaultLength
		if minutes, ok := parsePositiveSignal(quota.Signals[spec.lengthMinutesKey]); ok {
			length = time.Duration(minutes) * time.Minute
		}
		resetAt, ok := quotaWindowResetAt(spec, quota)
		if !ok {
			continue
		}
		if !resetAt.After(now) {
			periods := now.Sub(resetAt)/length + 1
			resetAt = resetAt.Add(periods * length)
		}
		windows = append(windows, quotaWindow{length: length, resetAt: resetAt})
	}
	sort.SliceStable(windows, func(i, j int) bool { return windows[i].length > windows[j].length })
	return windows
}

func quotaWindowResetAt(spec quotaWindowSpec, quota QuotaState) (time.Time, bool) {
	if unix, ok := parsePositiveSignal(quota.Signals[spec.resetAtKey]); ok {
		return time.Unix(unix, 0), true
	}
	if spec.resetAfterKey == "" || quota.ObservedAt.IsZero() {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(quota.Signals[spec.resetAfterKey])
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, false
	}
	return quota.ObservedAt.Add(time.Duration(seconds) * time.Second), true
}

func parsePositiveSignal(raw string) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

// quotaResetRank groups credentials before their windows are compared.
type quotaResetRank int

const (
	// quotaResetRankUnmeasured is a credential whose provider reports windows
	// but none is known yet, e.g. after a restart. It goes first so one
	// request measures it; otherwise it would never be used until every
	// measured credential is exhausted.
	quotaResetRankUnmeasured quotaResetRank = iota
	quotaResetRankMeasured
	// quotaResetRankUnreportable is a credential whose provider never reports
	// windows, so it is used only after every other credential.
	quotaResetRankUnreportable
)

func quotaResetRankOf(auth *Auth, windows []quotaWindow) quotaResetRank {
	switch {
	case len(windows) > 0:
		return quotaResetRankMeasured
	case providerReportsQuotaWindows(auth.Provider):
		return quotaResetRankUnmeasured
	default:
		return quotaResetRankUnreportable
	}
}

// providerReportsQuotaWindows reports whether quotaWindowsFor can ever return
// windows for the provider.
func providerReportsQuotaWindows(provider string) bool {
	return len(quotaWindowSpecs[strings.ToLower(strings.TrimSpace(provider))]) > 0
}

// soonestResetLess orders a before b when a's quota resets sooner. Windows are
// compared longest first, so the weekly reset decides before the 5h reset.
// Unmeasured credentials of a window-reporting provider sort first so they get
// measured, credentials whose provider never reports windows sort last, and
// the auth ID breaks ties so the order is deterministic.
func soonestResetLess(a, b *Auth, now time.Time) bool {
	if a == nil || b == nil {
		return a != nil
	}
	windowsA := quotaWindowsFor(a.Provider, a.Quota, now)
	windowsB := quotaWindowsFor(b.Provider, b.Quota, now)
	if rankA, rankB := quotaResetRankOf(a, windowsA), quotaResetRankOf(b, windowsB); rankA != rankB {
		return rankA < rankB
	}
	for i := 0; i < len(windowsA) || i < len(windowsB); i++ {
		if i >= len(windowsA) || i >= len(windowsB) {
			return i < len(windowsA)
		}
		if !windowsA[i].resetAt.Equal(windowsB[i].resetAt) {
			return windowsA[i].resetAt.Before(windowsB[i].resetAt)
		}
	}
	return a.ID < b.ID
}
