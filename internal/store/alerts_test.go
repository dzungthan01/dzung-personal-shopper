package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
)

func listAlerts(t *testing.T, store *Store, unreadOnly bool) []model.Alert {
	t.Helper()
	alerts, err := store.ListAlerts(context.Background(), unreadOnly, 0)
	require.NoError(t, err)
	return alerts
}

func pendingAlerts(t *testing.T, store *Store) []model.Alert {
	t.Helper()
	alerts, err := store.PendingNotifications(context.Background(), 0)
	require.NoError(t, err)
	return alerts
}

// seedItem inserts one item to hang alerts off.
func seedItem(t *testing.T, store *Store) *model.Item {
	t.Helper()
	item := &model.Item{
		URL: "https://cuyana.com/products/classic-easy-tote", Source: "shopify",
		Title: "Classic Easy Tote", Variant: "M",
	}
	_, err := store.AddItem(context.Background(), item)
	require.NoError(t, err)
	return item
}

func TestAddAlertDedupes(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	item := seedItem(t, store)

	alert := &model.Alert{
		ItemID: item.ID, Kind: model.AlertPriceDrop,
		DedupeKey: "item:1|kind:price_drop|to:17300|day:2026-09-09",
		Payload: model.AlertPayload{
			Title: "Classic Easy Tote", Currency: "USD",
			PriceCents: 17300, PreviousCents: 24800,
		},
	}

	id, created, err := store.AddAlert(ctx, alert)
	require.NoError(t, err)
	assert.True(t, created, "first insert should create a row")

	// The same key again is the mechanism working, not an error.
	duplicate := *alert
	_, created, err = store.AddAlert(ctx, &duplicate)
	require.NoError(t, err)
	assert.False(t, created, "a duplicate dedupe key should not create a row")

	// A further drop is a different key, so it must be stored.
	deeper := *alert
	deeper.DedupeKey = "item:1|kind:price_drop|to:15000|day:2026-09-09"
	deeper.Payload.PriceCents = 15000
	deeperID, created, err := store.AddAlert(ctx, &deeper)
	require.NoError(t, err)
	assert.True(t, created, "a lower price should be a new alert")
	assert.NotEqual(t, id, deeperID)
}

func TestAddAlertRequiresDedupeKey(t *testing.T) {
	store := openTestStore(t)
	item := seedItem(t, store)

	_, _, err := store.AddAlert(context.Background(), &model.Alert{ItemID: item.ID, Kind: model.AlertPriceDrop})
	assert.Error(t, err)
}

func TestAlertPayloadRoundTrips(t *testing.T) {
	store := openTestStore(t)
	item := seedItem(t, store)

	want := model.AlertPayload{
		Title: "Classic Easy Tote", URL: "https://cuyana.com/products/classic-easy-tote",
		Currency: "USD", Variant: "M", PriceCents: 17300, PreviousCents: 24800, CompareCents: 24800,
	}
	_, _, err := store.AddAlert(context.Background(), &model.Alert{
		ItemID: item.ID, Kind: model.AlertVariantBack, DedupeKey: "k1", Payload: want,
	})
	require.NoError(t, err)

	alerts := listAlerts(t, store, false)
	require.Len(t, alerts, 1)
	assert.Equal(t, want, alerts[0].Payload)
	assert.Equal(t, model.AlertVariantBack, alerts[0].Kind)
	assert.Equal(t, int64(7500), alerts[0].Payload.DropCents())
}

func TestPendingNotificationsAndMarkNotified(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	item := seedItem(t, store)

	base := time.Now().UTC().Add(-time.Hour)
	for i, key := range []string{"a", "b", "c"} {
		_, _, err := store.AddAlert(ctx, &model.Alert{
			ItemID: item.ID, Kind: model.AlertPriceDrop, DedupeKey: key,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
		require.NoError(t, err)
	}

	// Oldest first: a failed push should retry before newer alerts.
	pending := pendingAlerts(t, store)
	require.Len(t, pending, 3)
	assert.Equal(t, []string{"a", "b", "c"}, dedupeKeys(pending))

	require.NoError(t, store.MarkNotified(ctx, []int64{pending[0].ID, pending[1].ID}))

	assert.Equal(t, []string{"c"}, dedupeKeys(pendingAlerts(t, store)))

	all := listAlerts(t, store, false)
	require.Len(t, all, 3)
	for _, alert := range all {
		wantNotified := alert.DedupeKey != "c"
		assert.Equal(t, wantNotified, alert.Notified(), "alert %q", alert.DedupeKey)
	}
}

func TestAckAlertsIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	item := seedItem(t, store)

	var ids []int64
	for _, key := range []string{"a", "b"} {
		id, _, err := store.AddAlert(ctx, &model.Alert{
			ItemID: item.ID, Kind: model.AlertBackInStock, DedupeKey: key,
		})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.Len(t, listAlerts(t, store, true), 2)

	acked, err := store.AckAlerts(ctx, ids)
	require.NoError(t, err)
	assert.Equal(t, 2, acked)

	// Acking again changes nothing, and must not error.
	acked, err = store.AckAlerts(ctx, ids)
	require.NoError(t, err)
	assert.Equal(t, 0, acked)

	assert.Empty(t, listAlerts(t, store, true), "no alerts should remain unread")
	assert.Len(t, listAlerts(t, store, false), 2, "acking must not delete alerts")
}

func TestAckAlertsWithNoIDs(t *testing.T) {
	store := openTestStore(t)
	acked, err := store.AckAlerts(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, acked)
}

func TestAlertsCascadeWithItem(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	item := seedItem(t, store)

	_, _, err := store.AddAlert(ctx, &model.Alert{ItemID: item.ID, Kind: model.AlertPriceDrop, DedupeKey: "k"})
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, item.ID)
	require.NoError(t, err)

	assert.Empty(t, listAlerts(t, store, false), "alerts should cascade with their item")
}

func TestStampAlertsRejectsUnknownColumn(t *testing.T) {
	store := openTestStore(t)
	_, err := store.stampAlerts(context.Background(), "kind = 'x'; DROP TABLE alerts; --", []int64{1}, false)
	require.Error(t, err)

	// listAlerts fails the test if the table is gone: the guard must run before any SQL.
	listAlerts(t, store, false)
}

func dedupeKeys(alerts []model.Alert) []string {
	keys := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		keys = append(keys, alert.DedupeKey)
	}
	return keys
}
