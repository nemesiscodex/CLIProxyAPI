package quotahistory

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// firstPollDelay lets startup token refreshes settle before the first poll.
const firstPollDelay = 30 * time.Second

// FetchFunc sends a GET to url for a credential, replacing "$TOKEN$" in header values
// with the credential's token, and returns the upstream status code and body.
type FetchFunc func(ctx context.Context, auth *coreauth.Auth, url string, header map[string]string) (int, []byte, error)

// Poller records every enabled Claude and Codex credential's quota into a Store on a
// fixed interval, one credential at a time.
type Poller struct {
	parent context.Context
	store  *Store
	list   func() []*coreauth.Auth
	fetch  FetchFunc
	// firstDelay is the wait before the first poll after each start.
	firstDelay time.Duration

	mu       sync.Mutex
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewPoller returns a stopped poller. Polling never outlives parent.
func NewPoller(parent context.Context, store *Store, list func() []*coreauth.Auth, fetch FetchFunc) *Poller {
	if parent == nil {
		parent = context.Background()
	}
	return &Poller{parent: parent, store: store, list: list, fetch: fetch, firstDelay: firstPollDelay}
}

// Apply starts, restarts or stops polling to match the configuration. It does nothing
// when the poller already runs at interval, so unrelated config reloads keep the cadence.
func (p *Poller) Apply(enabled bool, interval time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if enabled && p.cancel != nil && p.interval == interval {
		return
	}
	p.stopLocked()
	if !enabled || interval <= 0 || p.parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(p.parent)
	done := make(chan struct{})
	p.interval, p.cancel, p.done = interval, cancel, done
	go p.run(ctx, interval, done)
	log.Infof("quota history polling started (interval=%s, file=%s)", interval, p.store.Path())
}

// Stop ends polling and waits for an in-flight poll to return.
func (p *Poller) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
}

func (p *Poller) stopLocked() {
	if p.cancel == nil {
		return
	}
	p.cancel()
	<-p.done
	p.interval, p.cancel, p.done = 0, nil, nil
	log.Info("quota history polling stopped")
}

func (p *Poller) run(ctx context.Context, interval time.Duration, done chan struct{}) {
	defer close(done)
	timer := time.NewTimer(p.firstDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		p.PollOnce(ctx)
		timer.Reset(interval)
	}
}

type pollTarget struct {
	name     string
	provider string
	source   usageSource
	auth     *coreauth.Auth
}

// targets returns the enabled Claude and Codex credentials, keyed by the same name the
// credentials list reports, sorted by name.
func (p *Poller) targets() []pollTarget {
	var targets []pollTarget
	for _, auth := range p.list() {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled || coreauth.IsConfigAPIKeyAuth(auth) {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		source, ok := usageSources[provider]
		if !ok {
			continue
		}
		name := strings.TrimSpace(auth.FileName)
		if name == "" {
			name = auth.ID
		}
		targets = append(targets, pollTarget{name: name, provider: provider, source: source, auth: auth})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].name < targets[j].name })
	return targets
}

// PollOnce reads every target once, records the readings, drops history of
// credentials that are gone or disabled, and saves the store.
func (p *Poller) PollOnce(ctx context.Context) {
	targets := p.targets()
	active := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		active[target.name] = struct{}{}
		if ctx.Err() != nil {
			break
		}
		remaining, errRead := p.read(ctx, target)
		if errRead != nil {
			if ctx.Err() == nil {
				log.WithFields(log.Fields{"credential": target.name, "provider": target.provider}).Warnf("quota history: poll failed: %v", errRead)
			}
			continue
		}
		p.store.Record(target.name, target.provider, remaining)
	}
	if ctx.Err() == nil {
		p.store.Prune(active)
	}
	if errSave := p.store.Save(); errSave != nil {
		log.WithError(errSave).Warn("quota history: failed to save")
	}
}

func (p *Poller) read(ctx context.Context, target pollTarget) (map[string]float64, error) {
	status, body, errFetch := p.fetch(ctx, target.auth, target.source.url, target.source.header(target.auth))
	if errFetch != nil {
		return nil, errFetch
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("usage endpoint returned status %d", status)
	}
	return target.source.parse(body)
}
