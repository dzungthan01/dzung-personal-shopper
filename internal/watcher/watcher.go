// Package watcher polls tracked items on a timer, records what it finds, and
// sends the alerts that result. It is the half of the tool that runs while no
// MCP client is connected.
package watcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	Logger   *slog.Logger

	PerStoreInterval time.Duration
	Burst            int
	Now              func() time.Time
}

// Watcher runs polling passes.
type Watcher struct {
	store    *store.Store
	sources  *source.Registry
	notifier notify.Notifier
	logger   *slog.Logger
	limiters *storeLimiters
	now      func() time.Time
}

// Result counts what one pass did. Checked and Failed are also stored in
// watcher_runs, which is what stats and doctor read.
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
		config.Logger = slog.New(slog.DiscardHandler)
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
		w.logger.Info("first pass scheduled", "delay", delay.Round(time.Second).String())
		if err := sleep(ctx, delay); err != nil {
			return nil // cancelled before starting: not a failure
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// RunOnce logs its own outcome; an error here is already recorded.
		_, _ = w.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce checks every active item, then sends whatever is still unsent, and
// records the pass in watcher_runs. One item failing does not stop the pass.
func (w *Watcher) RunOnce(ctx context.Context) (Result, error) {
	startedAt := w.now()
	recordContext, cancel := detached(ctx)
	runID, err := w.store.StartWatcherRun(recordContext, startedAt)
	cancel()
	if err != nil {
		// Losing the run log must not stop the polling it describes.
		w.logger.Error("could not record run start", "error", err)
	}
	w.logger.Info("watcher run started", "run_id", runID)

	result, err := w.pass(ctx)
	w.finishRun(ctx, runID, startedAt, result, err)
	return result, err
}

// detached outlives ctx's cancellation, so a pass cut short by a signal is
// still recorded, as interrupted.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// finishRun stores and logs how a pass ended.
func (w *Watcher) finishRun(ctx context.Context, runID int64, startedAt time.Time, result Result, passErr error) {
	finishedAt := w.now()
	summary := errorSummary(result, passErr)

	attributes := []any{
		"run_id", runID,
		"duration", finishedAt.Sub(startedAt).Round(time.Millisecond).String(),
		"checked", result.Checked, "skipped", result.Skipped, "failed", result.Failed,
		"alerts_raised", result.AlertsRaised, "pushed", result.Pushed, "push_failed", result.PushFailed,
	}
	switch {
	case errors.Is(passErr, context.Canceled):
		w.logger.Warn("watcher run interrupted", attributes...)
	case summary != "":
		w.logger.Error("watcher run failed", append(attributes, "error", summary)...)
	default:
		w.logger.Info("watcher run finished", attributes...)
	}

	if runID == 0 {
		return
	}
	recordContext, cancel := detached(ctx)
	defer cancel()
	err := w.store.FinishWatcherRun(recordContext, store.WatcherRun{
		ID: runID, FinishedAt: &finishedAt,
		ItemsChecked: result.Checked, ItemsFailed: result.Failed, ErrorSummary: summary,
	})
	if err != nil {
		w.logger.Error("could not record run finish", "run_id", runID, "error", err)
	}
}

// errorSummary is empty for a pass that worked. Some items failing is normal
// (a store is down); every item failing means the watcher is not working.
func errorSummary(result Result, passErr error) string {
	switch {
	case errors.Is(passErr, context.Canceled):
		return "interrupted before the pass finished"
	case passErr != nil:
		return passErr.Error()
	case result.Failed > 0 && result.Checked == 0:
		return fmt.Sprintf("all %d items failed; see the watcher log for reasons", result.Failed)
	default:
		return ""
	}
}

// pass does the work of RunOnce.
func (w *Watcher) pass(ctx context.Context) (Result, error) {
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
			w.logger.Warn("item check failed", "item_id", item.ID, "reason", err.Error())
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
		w.logger.Error("could not read pending alerts", "error", err)
		return 0, 0
	}

	for _, alert := range pending {
		if ctx.Err() != nil {
			return pushed, failed
		}
		if err := w.notifier.Notify(ctx, notify.FromAlert(alert)); err != nil {
			failed++
			w.logger.Warn("alert not sent", "alert_id", alert.ID, "reason", err.Error())
			continue
		}
		if err := w.store.MarkNotified(ctx, []int64{alert.ID}); err != nil {
			w.logger.Error("alert sent but not recorded", "alert_id", alert.ID, "error", err)
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
