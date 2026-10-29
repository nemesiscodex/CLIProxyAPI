package quotahistory

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The URLs, headers and window rules below mirror the management dashboard
// (Cli-Proxy-API-Management-Center src/utils/quota/constants.ts and
// src/features/quota/providers/{claude,codex}/data.ts) so both read the same numbers.

// usageSource describes how to read one provider's quota windows.
type usageSource struct {
	url string
	// header returns request headers; "$TOKEN$" in a value is replaced by the credential token.
	header func(auth *coreauth.Auth) map[string]string
	// parse maps a usage response body to remaining percent per window id.
	parse func(body []byte) (map[string]float64, error)
}

var usageSources = map[string]usageSource{
	"claude": {url: claudeUsageURL, header: claudeHeader, parse: parseClaudeUsage},
	"codex":  {url: codexUsageURL, header: codexHeader, parse: parseCodexUsage},
}

const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

var claudeRequestHeader = map[string]string{
	"User-Agent":     "claude-cli/2.1.280 (external, cli)",
	"Authorization":  "Bearer $TOKEN$",
	"Content-Type":   "application/json",
	"anthropic-beta": "oauth-2025-04-20",
}

// claudeWindows lists the account-wide windows; model-scoped ones are not recorded.
var claudeWindows = []struct{ key, id string }{
	{key: "five_hour", id: "five-hour"},
	{key: "seven_day", id: "seven-day"},
}

func claudeHeader(*coreauth.Auth) map[string]string {
	return cloneHeader(claudeRequestHeader)
}

// parseClaudeUsage reads each window's `utilization`, a used percent on 0..100.
func parseClaudeUsage(body []byte) (map[string]float64, error) {
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil, fmt.Errorf("decode claude usage: %w", errUnmarshal)
	}
	remaining := make(map[string]float64, len(claudeWindows))
	for _, window := range claudeWindows {
		fields, ok := payload[window.key].(map[string]any)
		if !ok {
			continue
		}
		if used, ok := numberValue(fields["utilization"]); ok {
			remaining[window.id] = remainingPercent(used)
		}
	}
	return remaining, nil
}

const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

var codexRequestHeader = map[string]string{
	"Authorization": "Bearer $TOKEN$",
	"Content-Type":  "application/json",
	"User-Agent":    "codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)",
}

const (
	codexFiveHourSeconds = 18000
	codexWeekSeconds     = 604800
	codexMinMonthSeconds = 28 * 24 * 60 * 60
	codexMaxMonthSeconds = 31 * 24 * 60 * 60
)

func codexHeader(auth *coreauth.Auth) map[string]string {
	header := cloneHeader(codexRequestHeader)
	if accountID := codexAccountID(auth); accountID != "" {
		header["Chatgpt-Account-Id"] = accountID
	}
	return header
}

// codexAccountID reads chatgpt_account_id from the id_token claims, the same claim the
// credentials list exposes and the dashboard sends.
func codexAccountID(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	idToken, _ := auth.Metadata["id_token"].(string)
	if strings.TrimSpace(idToken) == "" {
		return ""
	}
	claims, errParse := codex.ParseJWTToken(strings.TrimSpace(idToken))
	if errParse != nil || claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID)
}

// parseCodexUsage reads the main rate_limit only; code-review and additional limits
// are not recorded.
func parseCodexUsage(body []byte) (map[string]float64, error) {
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil, fmt.Errorf("decode codex usage: %w", errUnmarshal)
	}
	remaining := make(map[string]float64, 2)
	rateLimit, _ := firstValue(payload, "rate_limit", "rateLimit").(map[string]any)
	if rateLimit == nil {
		return remaining, nil
	}
	fiveHour, secondary := classifyCodexWindows(rateLimit)
	limitReached := truthy(firstValue(rateLimit, "limit_reached", "limitReached")) || rateLimit["allowed"] == false
	if value, ok := codexRemaining(fiveHour, limitReached); ok {
		remaining["five-hour"] = value
	}
	secondaryID := "weekly"
	if isCodexMonthlyWindow(secondary) {
		secondaryID = "monthly"
	}
	if value, ok := codexRemaining(secondary, limitReached); ok {
		remaining[secondaryID] = value
	}
	return remaining, nil
}

// classifyCodexWindows picks the five-hour and the weekly-or-monthly window by
// limit_window_seconds, then falls back to primary/secondary order for payloads
// that carry no durations.
func classifyCodexWindows(rateLimit map[string]any) (fiveHour, secondary map[string]any) {
	windows := [2]map[string]any{
		objectValue(firstValue(rateLimit, "primary_window", "primaryWindow")),
		objectValue(firstValue(rateLimit, "secondary_window", "secondaryWindow")),
	}
	fiveHourIndex, secondaryIndex := -1, -1
	for i, window := range windows {
		if window == nil {
			continue
		}
		seconds, ok := codexWindowSeconds(window)
		if ok && seconds == codexFiveHourSeconds && fiveHourIndex < 0 {
			fiveHourIndex = i
		} else if ok && (seconds == codexWeekSeconds || isCodexMonthSeconds(seconds)) && secondaryIndex < 0 {
			secondaryIndex = i
		}
	}
	if fiveHourIndex < 0 && windows[0] != nil && secondaryIndex != 0 {
		fiveHourIndex = 0
	}
	if secondaryIndex < 0 && windows[1] != nil && fiveHourIndex != 1 {
		secondaryIndex = 1
	}
	if fiveHourIndex >= 0 {
		fiveHour = windows[fiveHourIndex]
	}
	if secondaryIndex >= 0 {
		secondary = windows[secondaryIndex]
	}
	return fiveHour, secondary
}

// codexRemaining uses used_percent; without it, a reached limit with a known reset
// counts as fully used.
func codexRemaining(window map[string]any, limitReached bool) (float64, bool) {
	if window == nil {
		return 0, false
	}
	if used, ok := numberValue(firstValue(window, "used_percent", "usedPercent")); ok {
		return remainingPercent(used), true
	}
	if limitReached && codexResetKnown(window) {
		return 0, true
	}
	return 0, false
}

func codexResetKnown(window map[string]any) bool {
	if resetAt, ok := numberValue(firstValue(window, "reset_at", "resetAt")); ok && resetAt > 0 {
		return true
	}
	resetAfter, ok := numberValue(firstValue(window, "reset_after_seconds", "resetAfterSeconds"))
	return ok && resetAfter > 0
}

func codexWindowSeconds(window map[string]any) (float64, bool) {
	return numberValue(firstValue(window, "limit_window_seconds", "limitWindowSeconds"))
}

func isCodexMonthlyWindow(window map[string]any) bool {
	if window == nil {
		return false
	}
	seconds, ok := codexWindowSeconds(window)
	return ok && isCodexMonthSeconds(seconds)
}

func isCodexMonthSeconds(seconds float64) bool {
	return seconds >= codexMinMonthSeconds && seconds <= codexMaxMonthSeconds
}

func remainingPercent(used float64) float64 {
	return 100 - math.Max(0, math.Min(100, used))
}

func cloneHeader(header map[string]string) map[string]string {
	out := make(map[string]string, len(header)+1)
	for key, value := range header {
		out[key] = value
	}
	return out
}

// firstValue returns the first key whose value is present and not null, like `a ?? b`.
func firstValue(fields map[string]any, keys ...string) any {
	for _, key := range keys {
		if value := fields[key]; value != nil {
			return value
		}
	}
	return nil
}

func objectValue(value any) map[string]any {
	fields, _ := value.(map[string]any)
	return fields
}

// numberValue accepts a finite number or a numeric string, like the dashboard's normalizeNumberValue.
func numberValue(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, errParse := strconv.ParseFloat(trimmed, 64)
		if errParse != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

// truthy follows JavaScript Boolean() for JSON values.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed != ""
	default:
		return true
	}
}
