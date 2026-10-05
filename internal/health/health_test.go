package health

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// newDatabase creates a migrated database file and returns its path.
func newDatabase(t *testing.T) (string, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shopper.db")
	database, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { database.Close() })
	return path, database
}

func recordRun(t *testing.T, database *store.Store, startedAt time.Time, errorSummary string) {
	t.Helper()
	ctx := context.Background()
	id, err := database.StartWatcherRun(ctx, startedAt)
	require.NoError(t, err)
	finishedAt := startedAt.Add(time.Minute)
	require.NoError(t, database.FinishWatcherRun(ctx, store.WatcherRun{
		ID: id, FinishedAt: &finishedAt, ItemsChecked: 2, ErrorSummary: errorSummary,
	}))
}

func check(path string) Report {
	return Check(context.Background(), Options{DatabasePath: path, Now: func() time.Time { return now }})
}

func statuses(report Report) map[string]Status {
	byName := map[string]Status{}
	for _, result := range report.Checks {
		byName[result.Name] = result.Status
	}
	return byName
}

func TestCheckHealthy(t *testing.T) {
	path, database := newDatabase(t)
	recordRun(t, database, now.Add(-3*time.Hour), "")

	report := check(path)
	assert.Equal(t, map[string]Status{"database": Pass, "migrations": Pass, "watcher": Pass}, statuses(report))
	assert.True(t, report.Healthy)
	assert.Equal(t, "healthy", report.Verdict)
	assert.Equal(t, "last success 2 hours ago", report.Checks[2].Detail)
}

func TestCheckNoRunsYetWarns(t *testing.T) {
	path, _ := newDatabase(t)

	report := check(path)
	assert.Equal(t, Warn, statuses(report)["watcher"])
	assert.True(t, report.Healthy, "a warning is not a failure")
	assert.Equal(t, "healthy, with 1 warning", report.Verdict)
}

func TestCheckStaleWatcherFails(t *testing.T) {
	path, database := newDatabase(t)
	recordRun(t, database, now.Add(-48*time.Hour), "")
	recordRun(t, database, now.Add(-2*time.Hour), "all 2 items failed; see the watcher log for reasons")

	report := check(path)
	assert.Equal(t, Fail, statuses(report)["watcher"])
	assert.False(t, report.Healthy)
	assert.Equal(t, "unhealthy: 1 check failed", report.Verdict)
	assert.Equal(t, "last success 47 hours ago, over the 24h limit; latest run failed: all 2 items failed; see the watcher log for reasons",
		report.Checks[2].Detail)
}

func TestCheckMaxWatcherAge(t *testing.T) {
	path, database := newDatabase(t)
	recordRun(t, database, now.Add(-48*time.Hour), "")

	report := Check(context.Background(), Options{
		DatabasePath: path, MaxWatcherAge: 72 * time.Hour, Now: func() time.Time { return now },
	})
	assert.Equal(t, Pass, statuses(report)["watcher"])
}

func TestCheckOnlyFailedRunsFails(t *testing.T) {
	path, database := newDatabase(t)
	recordRun(t, database, now.Add(-time.Hour), "list items: database is locked")

	report := check(path)
	assert.Equal(t, Fail, statuses(report)["watcher"])
	assert.Equal(t, "the watcher has never completed a pass; latest run failed: list items: database is locked", report.Checks[2].Detail)
}

func TestCheckRecentFailureAfterSuccessWarns(t *testing.T) {
	path, database := newDatabase(t)
	recordRun(t, database, now.Add(-5*time.Hour), "")
	recordRun(t, database, now.Add(-time.Hour), "interrupted before the pass finished")

	assert.Equal(t, Warn, statuses(check(path))["watcher"])
}

func TestCheckMissingDatabaseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")

	report := check(path)
	assert.Equal(t, map[string]Status{"database": Fail, "migrations": Fail, "watcher": Fail}, statuses(report))
	assert.Contains(t, report.Checks[0].Detail, "run migrate")
	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist, "doctor must not create the database")
}

func TestCheckUnwritablePathFails(t *testing.T) {
	// A regular file where a directory should be: unwritable even for root,
	// which ignores permission bits.
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(parent, []byte("x"), 0o600))

	report := check(filepath.Join(parent, "shopper.db"))
	assert.Equal(t, Fail, statuses(report)["database"])
	assert.False(t, report.Healthy)
}

func TestCheckPendingMigrationFails(t *testing.T) {
	path, database := newDatabase(t)
	require.NoError(t, database.Close())

	// Roll the recorded version back so the newest migration reads as pending.
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`DELETE FROM goose_db_version WHERE version_id = (SELECT MAX(version_id) FROM goose_db_version)`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	report := check(path)
	assert.Equal(t, map[string]Status{"database": Pass, "migrations": Fail, "watcher": Fail}, statuses(report))
	assert.Contains(t, report.Checks[1].Detail, "run migrate")
}

func TestReportWriteTextAndJSON(t *testing.T) {
	path, database := newDatabase(t)
	recordRun(t, database, now.Add(-3*time.Hour), "")
	report := check(path)

	var text bytes.Buffer
	require.NoError(t, report.WriteText(&text))
	assert.Equal(t, "PASS  database    "+path+" is writable\n"+
		"PASS  migrations  schema at version 4, up to date\n"+
		"PASS  watcher     last success 2 hours ago\n"+
		"verdict: healthy\n", text.String())

	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, true, decoded["healthy"])
}

func TestCollect(t *testing.T) {
	path, database := newDatabase(t)
	ctx := context.Background()

	for _, variant := range []string{"Black", "Tan"} {
		item := &model.Item{URL: "https://cuyana.com/products/classic-easy-tote", Source: "shopify", Title: "Classic Easy Tote", Variant: variant}
		_, err := database.AddItem(ctx, item)
		require.NoError(t, err)
		_, err = database.AddObservation(ctx, &model.Observation{ItemID: item.ID, PriceCents: 24800, Currency: "USD"})
		require.NoError(t, err)
		_, _, err = database.AddAlert(ctx, &model.Alert{ItemID: item.ID, Kind: model.AlertPriceDrop, DedupeKey: variant})
		require.NoError(t, err)
	}
	_, err := database.AckAlerts(ctx, []int64{1})
	require.NoError(t, err)
	require.NoError(t, database.ArchiveItem(ctx, 2))

	recordRun(t, database, now.Add(-3*time.Hour), "")
	recordRun(t, database, now.Add(-2*time.Hour), "interrupted before the pass finished")
	recordRun(t, database, now.Add(-4*24*time.Hour), "")

	stats, err := Collect(ctx, database, path, now)
	require.NoError(t, err)

	assert.Equal(t, ItemStats{Active: 1, Archived: 1}, stats.Items)
	assert.Equal(t, 2, stats.Observations)
	assert.Equal(t, AlertStats{Pending: 1, Acknowledged: 1}, stats.Alerts)
	assert.Equal(t, 2, stats.Watcher.Runs24Hours)
	assert.Equal(t, 1, stats.Watcher.Failures24Hours)
	assert.Equal(t, 3, stats.Watcher.Runs7Days)
	assert.Equal(t, 1, stats.Watcher.Failures7Days)
	require.NotNil(t, stats.Watcher.LastSuccess)
	assert.Equal(t, now.Add(-3*time.Hour+time.Minute), *stats.Watcher.LastSuccess)
	require.NotNil(t, stats.Watcher.LastSuccessAgeSeconds)
	assert.Equal(t, int64((2*time.Hour + 59*time.Minute).Seconds()), *stats.Watcher.LastSuccessAgeSeconds)
	assert.Greater(t, stats.Database.SizeBytes, int64(0))
}

func TestStatsWriteText(t *testing.T) {
	lastSuccess := now.Add(-3 * time.Hour)
	stats := Stats{
		GeneratedAt:  now,
		Database:     DatabaseStats{Path: "/data/shopper.db", SizeBytes: 98304},
		Items:        ItemStats{Active: 12, Archived: 3},
		Observations: 418,
		Alerts:       AlertStats{Pending: 2, Acknowledged: 31},
		Watcher:      WatcherStats{Runs24Hours: 1, Runs7Days: 7, Failures7Days: 1, LastSuccess: &lastSuccess},
	}

	var text bytes.Buffer
	require.NoError(t, stats.WriteText(&text))
	assert.Equal(t, `database      /data/shopper.db (96.0 KB)
items         12 active, 3 archived
observations  418
alerts        2 pending, 31 acknowledged
watcher runs  1 in 24h (0 failed), 7 in 7d (1 failed)
last success  2026-10-05 09:00 UTC (3 hours ago)
`, text.String())

	stats.Watcher.LastSuccess = nil
	text.Reset()
	require.NoError(t, stats.WriteText(&text))
	assert.Contains(t, text.String(), "last success  never\n")
}

func TestDescribeAge(t *testing.T) {
	assert.Equal(t, "just now", DescribeAge(10*time.Second))
	assert.Equal(t, "1 minute ago", DescribeAge(time.Minute))
	assert.Equal(t, "45 minutes ago", DescribeAge(45*time.Minute))
	assert.Equal(t, "1 hour ago", DescribeAge(90*time.Minute))
	assert.Equal(t, "30 hours ago", DescribeAge(30*time.Hour))
}
