// Package watcher polls tracked items on a timer, records what it finds, and
// sends the alerts that result. It is the half of the tool that runs while no
// MCP client is connected.
package watcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/url"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/notify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/rules"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/manual"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// Defaults for how politely stores are polled: a trickle per store, with a
// small burst so a few items from one store go through without waiting.
const (
	DefaultPerStoreInterval = 2 * time.Second
	DefaultBurst            = 3
)

// Config collects the watcher's collaborators. Store, Sources and Notifier are required.
type Config struct {
	Store    *store.Store
	Sources  *source.Registry
	Notifier notify.Notifier
	Logger   *log.Logger

	PerStoreInterval time.Duration
	Burst            int
	Now              func() time.Time
}

// Watcher runs polling passes.
type Watcher struct {
	store    *store.Store
	sources  *source.Registry
	notifier notify.Notifier
	logger   *log.Logger
	limiters *storeLimiters
	now      func() time.Time
}

// Result counts what one pass did. It is what the logs report and what the
// planned stats subcommand will read.
type Result struct {
	Checked      int
	Skipped      int // manual items, which have no endpoint to poll
	Failed       int
	AlertsRaised int
	Pushed       int
	PushFailed   int
}

// New validates config and returns a Watcher.
func New(config Config) (*Watcher, error) {
	switch {
	case config.Store == nil:
		return nil, errors.New("watcher needs a store")
	case config.Sources == nil:
		return nil, errors.New("watcher needs sources")
	case config.Notifier == nil:
		return nil, errors.New("watcher needs a notifier")
	}
	if config.Logger == nil {
		config.Logger = log.New(io.Discard, "", 0)
	}
	if config.PerStoreInterval <= 0 {
		config.PerStoreInterval = DefaultPerStoreInterval
	}
	if config.Burst <= 0 {
		config.Burst = DefaultBurst
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}

	return &Watcher{
		store:    config.Store,
		sources:  config.Sources,
		notifier: config.Notifier,
		logger:   config.Logger,
		limiters: newStoreLimiters(rate.Every(config.PerStoreInterval), config.Burst),
		now:      config.Now,
	}, nil
}

// Run polls every interval until ctx is cancelled. The first pass waits a
// random part of jitter, so a machine that boots at the same time every day
// does not hit every store at the same moment.
func (w *Watcher) Run(ctx context.Context, interval, jitter time.Duration) error {
	if interval <= 0 {
		return errors.New("watch interval must be positive")
	}
	if jitter > 0 {
		delay := time.Duration(rand.Int63n(int64(jitter)))
		w.logger.Printf("first pass in %s", delay.Round(time.Second))
		if err := sleep(ctx, delay); err != nil {
			return nil // cancelled before starting: not a failure
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		result, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.logger.Printf("pass failed: %v", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		w.logger.Printf("pass done: %+v", result)

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce checks every active item, then sends whatever is still unsent.
// One item failing does not stop the pass.
func (w *Watcher) RunOnce(ctx context.Context) (Result, error) {
	var result Result

	items, err := w.store.ListItems(ctx, false)
	if err != nil {
		return result, fmt.Errorf("list items: %w", err)
	}

	for _, item := range items {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if item.Source == manual.Name {
			result.Skipped++
			continue
		}
		raised, err := w.checkItem(ctx, item)
		if err != nil {
			result.Failed++
			w.logger.Printf("item %d (%s): %v", item.ID, item.Title, err)
			continue
		}
		result.Checked++
		result.AlertsRaised += raised
	}

	pushed, failed := w.deliver(ctx)
	result.Pushed, result.PushFailed = pushed, failed
	return result, nil
}

// checkItem records one reading and stores any alerts it warrants.
func (w *Watcher) checkItem(ctx context.Context, item model.Item) (int, error) {
	productSource, err := w.sources.Get(item.Source)
	if err != nil {
		return 0, err
	}
	if err := w.limiters.wait(ctx, item.URL); err != nil {
		return 0, err
	}

	snapshot, err := productSource.Fetch(ctx, item.URL)
	if err != nil {
		return 0, err
	}

	previous, err := w.store.LatestObservation(ctx, item.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}

	current := snapshot.Observation(item.ID)
	if _, err := w.store.AddObservation(ctx, current); err != nil {
		return 0, err
	}

	var raised int
	for _, alert := range rules.Evaluate(item, previous, current, w.now()) {
		_, created, err := w.store.AddAlert(ctx, &alert)
		if err != nil {
			return raised, err
		}
		if created {
			raised++
		}
	}
	return raised, nil
}

// deliver sends every alert whose push has not gone out yet. An alert that
// fails keeps notified_at empty, so the next pass tries it again.
func (w *Watcher) deliver(ctx context.Context) (pushed, failed int) {
	pending, err := w.store.PendingNotifications(ctx, 0)
	if err != nil {
		w.logger.Printf("read pending alerts: %v", err)
		return 0, 0
	}

	for _, alert := range pending {
		if ctx.Err() != nil {
			return pushed, failed
		}
		if err := w.notifier.Notify(ctx, notify.FromAlert(alert)); err != nil {
			failed++
			w.logger.Printf("alert %d not sent: %v", alert.ID, err)
			continue
		}
		if err := w.store.MarkNotified(ctx, []int64{alert.ID}); err != nil {
			w.logger.Printf("alert %d sent but not recorded: %v", alert.ID, err)
		}
		pushed++
	}
	return pushed, failed
}

// sleep waits for delay unless ctx is cancelled first.
func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// storeLimiters keeps one rate limiter per host, so hammering one store does
// not slow requests to another.
type storeLimiters struct {
	mutex   sync.Mutex // guards perHost
	perHost map[string]*rate.Limiter
	every   rate.Limit
	burst   int
}

func newStoreLimiters(every rate.Limit, burst int) *storeLimiters {
	return &storeLimiters{perHost: map[string]*rate.Limiter{}, every: every, burst: burst}
}

// wait blocks until the host's limiter allows another request.
func (l *storeLimiters) wait(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	return l.forHost(parsed.Host).Wait(ctx)
}

func (l *storeLimiters) forHost(host string) *rate.Limiter {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	limiter, ok := l.perHost[host]
	if !ok {
		limiter = rate.NewLimiter(l.every, l.burst)
		l.perHost[host] = limiter
	}
	return limiter
}
