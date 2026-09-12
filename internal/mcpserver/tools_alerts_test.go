package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// seedAlert stores one alert as the watcher would.
func seedAlert(t *testing.T, database *store.Store, itemID int64, kind model.AlertKind, key string) int64 {
	t.Helper()
	id, created, err := database.AddAlert(context.Background(), &model.Alert{
		ItemID: itemID, Kind: kind, DedupeKey: key,
		Payload: model.AlertPayload{
			Title: "Classic Easy Tote", URL: "https://cuyana.com/products/classic-easy-tote",
			Currency: "USD", Variant: "M", PriceCents: 17300, PreviousCents: 24800,
		},
	})
	require.NoError(t, err)
	require.True(t, created)
	return id
}

func seedTrackedItem(t *testing.T, database *store.Store) int64 {
	t.Helper()
	item := &model.Item{
		URL: "https://cuyana.com/products/classic-easy-tote", Source: "manual",
		Title: "Classic Easy Tote", Variant: "M",
	}
	_, err := database.AddItem(context.Background(), item)
	require.NoError(t, err)
	return item.ID
}

func TestListAlertsUsesThePushWording(t *testing.T) {
	session, database := connectWithStore(t)
	itemID := seedTrackedItem(t, database)
	alertID := seedAlert(t, database, itemID, model.AlertPriceDrop, "k1")

	var listed listAlertsOutput
	call(t, session, "list_alerts", map[string]any{}, &listed)

	require.Len(t, listed.Alerts, 1)
	entry := listed.Alerts[0]
	assert.Equal(t, alertID, entry.AlertID)
	assert.Equal(t, itemID, entry.ItemID)
	assert.Equal(t, "price_drop", entry.Kind)
	assert.Equal(t, "Price drop: Classic Easy Tote (M)", entry.Summary)
	assert.Equal(t, "$248.00 → $173.00, down $75.00", entry.Detail)
	assert.Equal(t, "https://cuyana.com/products/classic-easy-tote", entry.URL)
	assert.False(t, entry.Pushed, "the watcher has not sent it yet")
	assert.False(t, entry.Read)
	assert.NotEmpty(t, entry.CreatedAt)
}

func TestAckAlertsHidesThemFromUnreadOnly(t *testing.T) {
	session, database := connectWithStore(t)
	itemID := seedTrackedItem(t, database)
	first := seedAlert(t, database, itemID, model.AlertPriceDrop, "k1")
	seedAlert(t, database, itemID, model.AlertVariantBack, "k2")

	var unread listAlertsOutput
	call(t, session, "list_alerts", map[string]any{"unread_only": true}, &unread)
	require.Equal(t, 2, unread.Count)

	var acked ackAlertsOutput
	call(t, session, "ack_alerts", map[string]any{"alert_ids": []int64{first}}, &acked)
	assert.Equal(t, 1, acked.Acknowledged)

	call(t, session, "list_alerts", map[string]any{"unread_only": true}, &unread)
	assert.Equal(t, 1, unread.Count, "the acknowledged alert is hidden")

	var all listAlertsOutput
	call(t, session, "list_alerts", map[string]any{}, &all)
	assert.Equal(t, 2, all.Count, "acknowledging does not delete anything")

	// Acking the same id again changes nothing and is not an error.
	call(t, session, "ack_alerts", map[string]any{"alert_ids": []int64{first}}, &acked)
	assert.Equal(t, 0, acked.Acknowledged)
}

func TestListAlertsRespectsLimit(t *testing.T) {
	session, database := connectWithStore(t)
	itemID := seedTrackedItem(t, database)
	for _, key := range []string{"k1", "k2", "k3"} {
		seedAlert(t, database, itemID, model.AlertPriceDrop, key)
	}

	var listed listAlertsOutput
	call(t, session, "list_alerts", map[string]any{"limit": 2}, &listed)
	assert.Equal(t, 2, listed.Count)
}

func TestAckAlertsRequiresIDs(t *testing.T) {
	session, _ := connectWithStore(t)
	result, err := session.CallTool(context.Background(),
		&mcp.CallToolParams{Name: "ack_alerts", Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.True(t, result.IsError, "no ids is a mistake worth reporting")
}
