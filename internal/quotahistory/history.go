// Package quotahistory records Claude and Codex quota percentages on a timer and keeps
// the samples on disk, so the management dashboard can project burn rates without a
// browser having been open while the quota was used.
package quotahistory

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// Retention is how long a sample is kept.
	Retention = 21 * 24 * time.Hour
	// MaxSamplesPerWindow caps the samples kept for one credential window.
	MaxSamplesPerWindow = 3000

	fileVersion = 1
)

// Sample is one reading of a quota window. It encodes as the JSON tuple
// [atMs, remaining] to keep the file and the API response small.
type Sample struct {
	// AtMs is the reading time in Unix milliseconds.
	AtMs int64
	// Remaining is the percent of the window left, 0..100.
	Remaining float64
}

// MarshalJSON encodes the sample as [atMs, remaining].
func (s Sample) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]any{s.AtMs, s.Remaining})
}

// UnmarshalJSON decodes a [atMs, remaining] tuple.
func (s *Sample) UnmarshalJSON(data []byte) error {
	var tuple []float64
	if errUnmarshal := json.Unmarshal(data, &tuple); errUnmarshal != nil {
		return errUnmarshal
	}
	if len(tuple) != 2 || math.IsNaN(tuple[0]) || math.IsInf(tuple[0], 0) || tuple[1] < 0 || tuple[1] > 100 {
		return fmt.Errorf("invalid quota sample %s", string(data))
	}
	s.AtMs, s.Remaining = int64(tuple[0]), tuple[1]
	return nil
}

// Account is one credential's history: samples per window id, each ascending by time.
type Account struct {
	Name     string              `json:"name"`
	Provider string              `json:"provider"`
	Windows  map[string][]Sample `json:"windows"`
}

type storedAccount struct {
	Provider string              `json:"provider"`
	Windows  map[string][]Sample `json:"windows"`
}

type storeFile struct {
	Version  int                       `json:"version"`
	Accounts map[string]*storedAccount `json:"accounts"`
}

// Store holds quota history keyed by credential name and window id. It is safe for
// concurrent use by the poller and the management endpoint.
type Store struct {
	path string
	now  func() time.Time

	mu       sync.Mutex
	accounts map[string]*storedAccount
}

// NewStore returns an empty store that persists to path. An empty path keeps the
// history in memory only. now stamps samples and drives retention.
func NewStore(path string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{path: path, now: now, accounts: make(map[string]*storedAccount)}
}

// DefaultPath returns the history file location: state/quota-history.json under
// WRITABLE_PATH when set, otherwise beside the config file. The subdirectory keeps
// the file out of an auth directory that shares the config directory, where the
// watcher would read any top-level .json file as a credential.
func DefaultPath(configPath string) string {
	base := util.WritablePath()
	if base == "" {
		configPath = strings.TrimSpace(configPath)
		if configPath == "" {
			return ""
		}
		base = filepath.Dir(configPath)
		if info, errStat := os.Stat(configPath); errStat == nil && info.IsDir() {
			base = configPath
		}
	}
	return filepath.Join(base, "state", "quota-history.json")
}

// Path returns the file the store persists to, or "" when it is memory-only.
func (s *Store) Path() string { return s.path }

// Load replaces the in-memory history with the saved file. A missing file is empty history.
func (s *Store) Load() error {
	if s.path == "" {
		return nil
	}
	data, errRead := os.ReadFile(s.path)
	if errors.Is(errRead, os.ErrNotExist) {
		return nil
	}
	if errRead != nil {
		return fmt.Errorf("read quota history: %w", errRead)
	}
	var file storeFile
	if errUnmarshal := json.Unmarshal(data, &file); errUnmarshal != nil {
		return fmt.Errorf("decode quota history %s: %w", s.path, errUnmarshal)
	}
	if file.Version != fileVersion {
		return fmt.Errorf("quota history %s has unsupported version %d", s.path, file.Version)
	}
	accounts := make(map[string]*storedAccount, len(file.Accounts))
	for name, account := range file.Accounts {
		if account == nil || len(account.Windows) == 0 {
			continue
		}
		for _, samples := range account.Windows {
			sortSamples(samples)
		}
		accounts[name] = account
	}
	s.mu.Lock()
	s.accounts = accounts
	s.mu.Unlock()
	return nil
}

// Record appends one sample per window for a credential, stamped with the store clock.
// Every reading is kept, including unchanged ones: an unchanged value is evidence of
// zero burn.
func (s *Store) Record(name, provider string, remaining map[string]float64) {
	if len(remaining) == 0 {
		return
	}
	at := s.now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[name]
	if account == nil || account.Provider != provider {
		account = &storedAccount{Provider: provider, Windows: make(map[string][]Sample)}
		s.accounts[name] = account
	}
	for window, value := range remaining {
		samples := append(account.Windows[window], Sample{AtMs: at, Remaining: value})
		if n := len(samples); n > 1 && samples[n-2].AtMs > at {
			sortSamples(samples)
		}
		if len(samples) > MaxSamplesPerWindow {
			samples = append([]Sample(nil), samples[len(samples)-MaxSamplesPerWindow:]...)
		}
		account.Windows[window] = samples
	}
}

// Prune drops credentials missing from active and samples older than Retention.
func (s *Store) Prune(active map[string]struct{}) {
	cutoff := s.now().Add(-Retention).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, account := range s.accounts {
		if _, ok := active[name]; !ok {
			delete(s.accounts, name)
			continue
		}
		for window, samples := range account.Windows {
			first := sort.Search(len(samples), func(i int) bool { return samples[i].AtMs >= cutoff })
			if first == len(samples) {
				delete(account.Windows, window)
				continue
			}
			account.Windows[window] = samples[first:]
		}
		if len(account.Windows) == 0 {
			delete(s.accounts, name)
		}
	}
}

// Accounts returns a copy of the history sorted by credential name.
func (s *Store) Accounts() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Account, 0, len(s.accounts))
	for name, account := range s.accounts {
		windows := make(map[string][]Sample, len(account.Windows))
		for window, samples := range account.Windows {
			windows[window] = append([]Sample(nil), samples...)
		}
		out = append(out, Account{Name: name, Provider: account.Provider, Windows: windows})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Save writes the history atomically: a temp file in the same directory, then a rename.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	data, errMarshal := json.Marshal(storeFile{Version: fileVersion, Accounts: s.accounts})
	s.mu.Unlock()
	if errMarshal != nil {
		return fmt.Errorf("encode quota history: %w", errMarshal)
	}
	dir := filepath.Dir(s.path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create quota history directory: %w", errMkdir)
	}
	tmp, errCreate := os.CreateTemp(dir, filepath.Base(s.path)+".*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create quota history temp file: %w", errCreate)
	}
	tmpPath := tmp.Name()
	_, errWrite := tmp.Write(data)
	errClose := tmp.Close()
	if errWrite != nil || errClose != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write quota history temp file: %w", errors.Join(errWrite, errClose))
	}
	if errRename := os.Rename(tmpPath, s.path); errRename != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace quota history file: %w", errRename)
	}
	return nil
}

func sortSamples(samples []Sample) {
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].AtMs < samples[j].AtMs })
}
