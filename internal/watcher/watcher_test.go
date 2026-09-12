package watcher

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/notify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/manual"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/shopify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// recorder is a Notifier that remembers messages and can be made to fail.
type recorder struct {
	mutex    sync.Mutex
	messages []notify.Message
	err      error
}

func (r *recorder) Notify(_ context.Context, message notify.Message) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.err != nil {
		return r.err
	}
	r.messages = append(r.messages, message)
	return nil
}

func (r *recorder) sent() []notify.Message {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return append([]notify.Message(nil), r.messages...)
}

// storefront serves a Shopify-shaped product whose price can change mid-test.
type storefront struct {
	*httptest.Server
	mutex      sync.Mutex
	priceCents int
	inStock    bool
}

func newStorefront(t *testing.T) *storefront {
	t.Helper()
	front := &storefront{priceCents: 24800, inStock: true}
	front.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		front.mutex.Lock()
		price, inStock := front.priceCents, front.inStock
		front.mutex.Unlock()

		switch r.URL.Path {
		case "/meta.json":
			_, _ = w.Write([]byte(`{"currency":"USD","name":"Cuyana","myshopify_domain":"cuyana.myshopify.com"}`))
		case "/products/classic-easy-tote.js":
			_, _ = w.Write([]byte(`{"title":"Classic Easy Tote","vendor":"Cuyana","price":` +
				strconv.Itoa(price) + `,"available":` + btoa(inStock) + `,
				"options":[{"name":"Size","position":1}],
				"variants":[{"title":"M","option1":"M","sku":"T-M","price":` + strconv.Itoa(price) +
				`,"available":` + btoa(inStock) + `}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(front.Close)
	return front
}

func (f *storefront) setPrice(cents int) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.priceCents = cents
}

func (f *storefront) setStock(inStock bool) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.inStock = inStock
}

func btoa(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// newWatcher wires a real store and source against the test storefront.
func newWatcher(t *testing.T, front *storefront, notifier notify.Notifier) (*Watcher, *store.Store) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "watch.db"))
	require.NoError(t, err)
	t.Cleanup(func() { database.Close() })

	watcher, err := New(Config{
		Store:            database,
		Sources:          source.NewRegistry(shopify.New(front.Client(), ""), manual.New()),
		Notifier:         notifier,
		PerStoreInterval: time.Millisecond, // keep tests fast
	})
	require.NoError(t, err)
	return watcher, database
}

func addItem(t *testing.T, database *store.Store, front *storefront, sourceName string) *model.Item {
	t.Helper()
	item := &model.Item{
		URL: front.URL + "/products/classic-easy-tote", Source: sourceName,
		Title: "Classic Easy Tote", Variant: "M",
	}
	_, err := database.AddItem(context.Background(), item)
	require.NoError(t, err)
	return item
}

func TestRunOnceRecordsAndAlertsOnAPriceDrop(t *testing.T) {
	front := newStorefront(t)
	notifier := &recorder{}
	watcher, database := newWatcher(t, front, notifier)
	item := addItem(t, database, front, shopify.Name)
	ctx := context.Background()

	// First pass is a baseline: a reading, no alert.
	result, err := watcher.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Checked)
	assert.Equal(t, 0, result.AlertsRaised, "the first reading is a baseline")
	assert.Empty(t, notifier.sent())

	front.setPrice(17300)

	result, err = watcher.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.AlertsRaised)
	assert.Equal(t, 1, result.Pushed)

	sent := notifier.sent()
	require.Len(t, sent, 1)
	assert.Equal(t, "Price drop: Classic Easy Tote (M)", sent[0].Title)
	assert.Equal(t, "$248.00 → $173.00, down $75.00", sent[0].Body)

	history, err := database.ObservationHistory(ctx, item.ID, 0)
	require.NoError(t, err)
	assert.Len(t, history, 2, "both readings are kept")
}

func TestRunOnceSkipsManualItems(t *testing.T) {
	front := newStorefront(t)
	watcher, database := newWatcher(t, front, &recorder{})
	addItem(t, database, front, manual.Name)

	result, err := watcher.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Skipped)
	assert.Equal(t, 0, result.Checked, "manual items have no endpoint to poll")
}

func TestFailedPushIsRetriedNextPass(t *testing.T) {
	front := newStorefront(t)
	notifier := &recorder{err: errors.New("phone is off")}
	watcher, database := newWatcher(t, front, notifier)
	addItem(t, database, front, shopify.Name)
	ctx := context.Background()

	require.NoError(t, firstPass(t, watcher, ctx))
	front.setPrice(17300)

	result, err := watcher.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.PushFailed)
	assert.Empty(t, notifier.sent())

	pending, err := database.PendingNotifications(ctx, 0)
	require.NoError(t, err)
	require.Len(t, pending, 1, "a failed push must stay pending")

	// The phone comes back; the next pass delivers it without a new price change.
	notifier.err = nil
	result, err = watcher.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Pushed)
	assert.Len(t, notifier.sent(), 1)

	pending, err = database.PendingNotifications(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestRestockRaisesOneAlertPerPass(t *testing.T) {
	front := newStorefront(t)
	notifier := &recorder{}
	watcher, database := newWatcher(t, front, notifier)
	addItem(t, database, front, shopify.Name)
	ctx := context.Background()

	front.setStock(false)
	require.NoError(t, firstPass(t, watcher, ctx))

	front.setStock(true)
	result, err := watcher.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.AlertsRaised)
	require.Len(t, notifier.sent(), 1)
	assert.Equal(t, "Back in M: Classic Easy Tote", notifier.sent()[0].Title)

	// Still in stock next pass: nothing new, and no second push.
	result, err = watcher.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, result.AlertsRaised)
	assert.Len(t, notifier.sent(), 1)
}

func TestRunStopsOnContextCancel(t *testing.T) {
	front := newStorefront(t)
	watcher, database := newWatcher(t, front, &recorder{})
	addItem(t, database, front, shopify.Name)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx, time.Hour, 0) }()

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "cancellation is a clean stop, not a failure")
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestNewRejectsMissingCollaborators(t *testing.T) {
	_, err := New(Config{})
	assert.Error(t, err)
	_, err = New(Config{Store: &store.Store{}, Sources: source.NewRegistry()})
	assert.Error(t, err, "a notifier is required")
}

func firstPass(t *testing.T, watcher *Watcher, ctx context.Context) error {
	t.Helper()
	_, err := watcher.RunOnce(ctx)
	return err
}
