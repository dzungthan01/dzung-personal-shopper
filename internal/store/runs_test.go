package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
)

func recordRun(t *testing.T, store *Store, startedAt time.Time, errorSummary string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := store.StartWatcherRun(ctx, startedAt)
	require.NoError(t, err)
	finishedAt := startedAt.Add(time.Minute)
	require.NoError(t, store.FinishWatcherRun(ctx, WatcherRun{
		ID: id, FinishedAt: &finishedAt, ItemsChecked: 3, ItemsFailed: 1, ErrorSummary: errorSummary,
	}))
	return id
}

func TestLastSuccessfulWatcherRunSkipsFailedAndUnfinished(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	_, err := store.LastSuccessfulWatcherRun(ctx)
	assert.True(t, errors.Is(err, ErrNotFound), "no runs yet: %v", err)

	successID := recordRun(t, store, now.Add(-3*time.Hour), "")
	recordRun(t, store, now.Add(-2*time.Hour), "list items: database is locked")
	_, err = store.StartWatcherRun(ctx, now.Add(-time.Hour)) // died mid-pass
	require.NoError(t, err)

	latest, err := store.LatestWatcherRun(ctx)
	require.NoError(t, err)
	assert.Nil(t, latest.FinishedAt, "the newest run is the unfinished one")

	run, err := store.LastSuccessfulWatcherRun(ctx)
	require.NoError(t, err)
	assert.Equal(t, successID, run.ID)
	assert.Equal(t, now.Add(-3*time.Hour), run.StartedAt)
	assert.Equal(t, 3, run.ItemsChecked)
	assert.Equal(t, 1, run.ItemsFailed)
	assert.True(t, run.Succeeded())
}

func TestCountWatcherRuns(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	recordRun(t, store, now.Add(-time.Hour), "")
	recordRun(t, store, now.Add(-2*time.Hour), "interrupted")
	recordRun(t, store, now.Add(-72*time.Hour), "all 2 items failed")
	recordRun(t, store, now.Add(-30*24*time.Hour), "")

	runs, failures, err := store.CountWatcherRuns(context.Background(), now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, runs)
	assert.Equal(t, 1, failures)

	runs, failures, err = store.CountWatcherRuns(context.Background(), now.Add(-7*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 3, runs)
	assert.Equal(t, 2, failures)
}

func TestFinishWatcherRunCapsSummary(t *testing.T) {
	store := openTestStore(t)
	long := "GET https://cuyana.com/products/classic-easy-tote.js:\n" + strings.Repeat("x", 500)
	id := recordRun(t, store, time.Now().UTC(), long)

	var summary string
	require.NoError(t, store.db.QueryRow(`SELECT error_summary FROM watcher_runs WHERE id = ?`, id).Scan(&summary))
	assert.Equal(t, MaxErrorSummary, len([]rune(summary)))
	assert.NotContains(t, summary, "\n", "the summary is one line")
}

func TestFinishWatcherRunUnknownID(t *testing.T) {
	store := openTestStore(t)
	finishedAt := time.Now().UTC()
	err := store.FinishWatcherRun(context.Background(), WatcherRun{ID: 99, FinishedAt: &finishedAt})
	assert.True(t, errors.Is(err, ErrNotFound), "got %v", err)
}

func TestCount(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	var itemIDs []int64
	for _, variant := range []string{"Black", "Tan", "Navy"} {
		item := &model.Item{URL: "https://cuyana.com/products/classic-easy-tote", Source: "shopify", Title: "Tote", Variant: variant}
		id, err := store.AddItem(ctx, item)
		require.NoError(t, err)
		itemIDs = append(itemIDs, id)
	}
	require.NoError(t, store.ArchiveItem(ctx, itemIDs[2]))
	for range 4 {
		_, err := store.AddObservation(ctx, &model.Observation{ItemID: itemIDs[0], PriceCents: 24800, Currency: "USD"})
		require.NoError(t, err)
	}
	for index, key := range []string{"a", "b", "c"} {
		alert := &model.Alert{ItemID: itemIDs[0], Kind: model.AlertPriceDrop, DedupeKey: key}
		_, _, err := store.AddAlert(ctx, alert)
		require.NoError(t, err)
		if index == 0 {
			_, err := store.AckAlerts(ctx, []int64{alert.ID})
			require.NoError(t, err)
		}
	}

	counts, err := store.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, Counts{ItemsActive: 2, ItemsArchived: 1, Observations: 4, AlertsPending: 2, AlertsAcknowledged: 1}, counts)
}

func TestOpenExistingDoesNotMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	_, err := OpenExisting(path)
	assert.Error(t, err, "a missing database is not created")

	migrated, err := Open(path)
	require.NoError(t, err)
	_, err = migrated.db.Exec(`DELETE FROM goose_db_version WHERE version_id = 4`)
	require.NoError(t, err)
	require.NoError(t, migrated.Close())

	existing, err := OpenExisting(path)
	require.NoError(t, err)
	t.Cleanup(func() { existing.Close() })

	current, latest, err := existing.SchemaVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), current, "OpenExisting must leave a pending migration pending")
	assert.Equal(t, int64(4), latest)
}
