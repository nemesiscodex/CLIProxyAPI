package quotahistory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.UnixMilli(1761700000000)}
	return NewStore(filepath.Join(t.TempDir(), "state", "quota-history.json"), clock.Now), clock
}

func activeSet(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

func TestStoreKeepsEveryReadingIncludingUnchangedOnes(t *testing.T) {
	store, clock := newTestStore(t)
	store.Record("a.json", "claude", map[string]float64{"seven-day": 61})
	clock.Advance(15 * time.Minute)
	store.Record("a.json", "claude", map[string]float64{"seven-day": 61})

	want := []Account{{Name: "a.json", Provider: "claude", Windows: map[string][]Sample{
		"seven-day": {{AtMs: 1761700000000, Remaining: 61}, {AtMs: 1761700900000, Remaining: 61}},
	}}}
	if got := store.Accounts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("accounts = %+v, want %+v", got, want)
	}
}

func TestStoreDropsSamplesOlderThanRetention(t *testing.T) {
	store, clock := newTestStore(t)
	store.Record("a.json", "codex", map[string]float64{"weekly": 90, "five-hour": 50})
	clock.Advance(Retention - time.Hour)
	store.Record("a.json", "codex", map[string]float64{"weekly": 80})
	clock.Advance(2 * time.Hour)
	store.Prune(activeSet("a.json"))

	want := []Account{{Name: "a.json", Provider: "codex", Windows: map[string][]Sample{
		"weekly": {{AtMs: clock.Now().Add(-time.Hour * 2).UnixMilli(), Remaining: 80}},
	}}}
	if got := store.Accounts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("accounts = %+v, want %+v", got, want)
	}
}

func TestStoreCapsSamplesPerWindowKeepingTheNewest(t *testing.T) {
	store, clock := newTestStore(t)
	for i := 0; i < MaxSamplesPerWindow+5; i++ {
		store.Record("a.json", "claude", map[string]float64{"five-hour": float64(i % 100)})
		clock.Advance(time.Second)
	}
	samples := store.Accounts()[0].Windows["five-hour"]
	if len(samples) != MaxSamplesPerWindow {
		t.Fatalf("kept %d samples, want %d", len(samples), MaxSamplesPerWindow)
	}
	if first := samples[0].AtMs; first != 1761700000000+5000 {
		t.Fatalf("oldest kept sample at %d, want the sixth reading", first)
	}
}

func TestStorePruneDropsRemovedCredentials(t *testing.T) {
	store, _ := newTestStore(t)
	store.Record("kept.json", "claude", map[string]float64{"five-hour": 70})
	store.Record("removed.json", "codex", map[string]float64{"weekly": 40})
	store.Prune(activeSet("kept.json"))

	accounts := store.Accounts()
	if len(accounts) != 1 || accounts[0].Name != "kept.json" {
		t.Fatalf("accounts = %+v, want only kept.json", accounts)
	}
}

func TestStorePersistsAndReloads(t *testing.T) {
	store, clock := newTestStore(t)
	store.Record("a.json", "claude", map[string]float64{"five-hour": 88, "seven-day": 61})
	clock.Advance(15 * time.Minute)
	store.Record("b.json", "codex", map[string]float64{"weekly": 60.5})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded := NewStore(store.Path(), clock.Now)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if got, want := reloaded.Accounts(), store.Accounts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded = %+v, want %+v", got, want)
	}
	entries, err := os.ReadDir(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("state directory holds %d entries, want only the history file", len(entries))
	}
}

func TestStoreLoadTreatsMissingFileAsEmpty(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Load(); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if got := store.Accounts(); len(got) != 0 {
		t.Fatalf("accounts = %+v, want none", got)
	}
}

func TestSampleEncodesAsTimeRemainingTuple(t *testing.T) {
	data, err := json.Marshal([]Sample{{AtMs: 1761700000000, Remaining: 61}, {AtMs: 1761700900000, Remaining: 60.5}})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `[[1761700000000,61],[1761700900000,60.5]]` {
		t.Fatalf("encoded = %s", data)
	}
}

func TestDefaultPathKeepsHistoryOutOfTheConfigDirectoryTopLevel(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")
	dir := t.TempDir()
	if got, want := DefaultPath(filepath.Join(dir, "config.yaml")), filepath.Join(dir, "state", "quota-history.json"); got != want {
		t.Fatalf("path = %s, want %s", got, want)
	}
	writable := t.TempDir()
	t.Setenv("WRITABLE_PATH", writable)
	if got, want := DefaultPath(filepath.Join(dir, "config.yaml")), filepath.Join(writable, "state", "quota-history.json"); got != want {
		t.Fatalf("path with WRITABLE_PATH = %s, want %s", got, want)
	}
}
