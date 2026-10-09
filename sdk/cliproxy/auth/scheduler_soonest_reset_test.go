package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func soonestResetFixtureAuths(now time.Time) []*Auth {
	return []*Auth{
		claudeQuotaAuth("a-weekly-late", now.Add(time.Hour), now.Add(72*time.Hour)),
		claudeQuotaAuth("b-weekly-soon", now.Add(4*time.Hour), now.Add(24*time.Hour)),
		{ID: "0-no-signals", Provider: "claude"},
	}
}

func TestSchedulerPick_SoonestResetMatchesSelector(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auths := soonestResetFixtureAuths(now)
	selectorPick, errSelector := (&SoonestResetSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if errSelector != nil {
		t.Fatalf("selector Pick() error = %v", errSelector)
	}

	scheduler := newSchedulerForTest(&SoonestResetSelector{}, soonestResetFixtureAuths(now)...)
	for index := 0; index < 3; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil || got.ID != "0-no-signals" || got.ID != selectorPick.ID {
			t.Fatalf("pickSingle() #%d auth = %s, selector = %s, want 0-no-signals", index, idOf(got), selectorPick.ID)
		}
	}

	tried := map[string]struct{}{"0-no-signals": {}}
	for _, want := range []string{"b-weekly-soon", "a-weekly-late"} {
		got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, tried)
		if errPick != nil {
			t.Fatalf("pickSingle() with tried %v error = %v", tried, errPick)
		}
		if got == nil || got.ID != want {
			t.Fatalf("pickSingle() with tried %v auth = %s, want %s", tried, idOf(got), want)
		}
		tried[want] = struct{}{}
	}
}

func TestSchedulerPick_MixedProvidersSoonestReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	codex := &Auth{
		ID:       "z-codex",
		Provider: "codex",
		Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Reset-After-Seconds":   "60",
			"X-Codex-Secondary-Reset-After-Seconds": "3600",
		}},
	}
	measured := soonestResetFixtureAuths(now)[:2]
	scheduler := newSchedulerForTest(&SoonestResetSelector{}, append(measured, codex)...)

	got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"claude", "codex"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickMixed() error = %v", errPick)
	}
	if got == nil || got.ID != "z-codex" || provider != "codex" {
		t.Fatalf("pickMixed() = %s/%s, want codex/z-codex", provider, idOf(got))
	}
}

func TestManager_SoonestResetUsesSignalsObservedOnAnotherModel(t *testing.T) {
	const (
		observedModel = "claude-soonest-observed"
		pickedModel   = "claude-soonest-picked"
	)
	reg := registry.GetGlobalRegistry()
	for _, authID := range []string{"soonest-a", "soonest-b"} {
		reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: observedModel}, {ID: pickedModel}})
	}
	t.Cleanup(func() {
		reg.UnregisterClient("soonest-a")
		reg.UnregisterClient("soonest-b")
	})

	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	for _, authID := range []string{"soonest-a", "soonest-b"} {
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "claude"}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", authID, errRegister)
		}
	}
	got, errPick := manager.scheduler.pickSingle(context.Background(), "claude", pickedModel, cliproxyexecutor.Options{}, nil)
	if errPick != nil || got == nil || got.ID != "soonest-a" {
		t.Fatalf("pickSingle() before observation = %s, %v; want soonest-a", idOf(got), errPick)
	}

	now := time.Now()
	observe := func(authID string, weeklyReset time.Time) {
		ctx := internallogging.WithResponseHeadersHolder(context.Background())
		internallogging.SetResponseHeaders(ctx, http.Header{
			"Anthropic-Ratelimit-Unified-5h-Reset": []string{unixSignal(now.Add(time.Hour))},
			"Anthropic-Ratelimit-Unified-7d-Reset": []string{unixSignal(weeklyReset)},
		})
		manager.MarkResult(ctx, Result{AuthID: authID, Provider: "claude", Model: observedModel, Success: true})
	}

	observe("soonest-a", now.Add(24*time.Hour))
	got, errPick = manager.scheduler.pickSingle(context.Background(), "claude", pickedModel, cliproxyexecutor.Options{}, nil)
	if errPick != nil || got == nil || got.ID != "soonest-b" {
		t.Fatalf("pickSingle() after measuring soonest-a on %s = %s, %v; want unmeasured soonest-b", observedModel, idOf(got), errPick)
	}

	observe("soonest-b", now.Add(48*time.Hour))
	got, errPick = manager.scheduler.pickSingle(context.Background(), "claude", pickedModel, cliproxyexecutor.Options{}, nil)
	if errPick != nil || got == nil || got.ID != "soonest-a" {
		t.Fatalf("pickSingle() after measuring both on %s = %s, %v; want soonest-a", observedModel, idOf(got), errPick)
	}
}
