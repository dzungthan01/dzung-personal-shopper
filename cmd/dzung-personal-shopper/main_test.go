package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// seededDatabase returns a database with two items (one archived), three
// observations, two alerts (one read) and two recent watcher runs, one failed.
func seededDatabase(t *testing.T, lastSuccess time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shopper.db")
	database, err := store.Open(path)
	require.NoError(t, err)
	defer database.Close()
	ctx := context.Background()

	for _, variant := range []string{"XS", "S"} {
		item := &model.Item{URL: "https://girlfriend.com/products/float-legging", Source: "shopify", Title: "FLOAT Legging", Variant: variant}
		_, err := database.AddItem(ctx, item)
		require.NoError(t, err)
		_, err = database.AddObservation(ctx, &model.Observation{ItemID: item.ID, PriceCents: 7800, Currency: "USD"})
		require.NoError(t, err)
		_, _, err = database.AddAlert(ctx, &model.Alert{ItemID: item.ID, Kind: model.AlertPriceDrop, DedupeKey: variant})
		require.NoError(t, err)
	}
	_, err = database.AddObservation(ctx, &model.Observation{ItemID: 1, PriceCents: 6800, Currency: "USD"})
	require.NoError(t, err)
	_, err = database.AckAlerts(ctx, []int64{1})
	require.NoError(t, err)
	require.NoError(t, database.ArchiveItem(ctx, 2))

	for _, summary := range []string{"", "all 1 items failed; see the watcher log for reasons"} {
		startedAt := lastSuccess
		if summary != "" {
			startedAt = lastSuccess.Add(30 * time.Minute)
		}
		id, err := database.StartWatcherRun(ctx, startedAt)
		require.NoError(t, err)
		finishedAt := startedAt.Add(time.Minute)
		require.NoError(t, database.FinishWatcherRun(ctx, store.WatcherRun{
			ID: id, FinishedAt: &finishedAt, ItemsChecked: 1, ErrorSummary: summary,
		}))
	}
	return path
}

func TestStatsText(t *testing.T) {
	path := seededDatabase(t, time.Now().UTC().Add(-3*time.Hour))

	var output bytes.Buffer
	require.NoError(t, run([]string{"stats", "--db", path}, &output))

	text := output.String()
	assert.Contains(t, text, "items         1 active, 1 archived\n")
	assert.Contains(t, text, "observations  3\n")
	assert.Contains(t, text, "alerts        1 pending, 1 acknowledged\n")
	assert.Contains(t, text, "watcher runs  2 in 24h (1 failed), 2 in 7d (1 failed)\n")
	assert.Contains(t, text, "(2 hours ago)\n")
}

func TestStatsJSON(t *testing.T) {
	path := seededDatabase(t, time.Now().UTC().Add(-3*time.Hour))

	var output bytes.Buffer
	require.NoError(t, run([]string{"stats", "--db", path, "--json"}, &output))

	var stats struct {
		Database struct {
			Path      string `json:"path"`
			SizeBytes int64  `json:"size_bytes"`
		} `json:"database"`
		Items        map[string]int `json:"items"`
		Observations int            `json:"observations"`
		Alerts       map[string]int `json:"alerts"`
		Watcher      struct {
			Runs24Hours     int        `json:"runs_24h"`
			Failures24Hours int        `json:"failures_24h"`
			Runs7Days       int        `json:"runs_7d"`
			Failures7Days   int        `json:"failures_7d"`
			LastSuccess     *time.Time `json:"last_success"`
		} `json:"watcher"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &stats), output.String())

	assert.Equal(t, path, stats.Database.Path)
	assert.Greater(t, stats.Database.SizeBytes, int64(0))
	assert.Equal(t, map[string]int{"active": 1, "archived": 1}, stats.Items)
	assert.Equal(t, 3, stats.Observations)
	assert.Equal(t, map[string]int{"pending": 1, "acknowledged": 1}, stats.Alerts)
	assert.Equal(t, 2, stats.Watcher.Runs24Hours)
	assert.Equal(t, 1, stats.Watcher.Failures24Hours)
	assert.Equal(t, 2, stats.Watcher.Runs7Days)
	assert.Equal(t, 1, stats.Watcher.Failures7Days)
	assert.NotNil(t, stats.Watcher.LastSuccess)
}

func TestStatsMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	err := run([]string{"stats", "--db", path}, io.Discard)
	assert.ErrorContains(t, err, "run migrate")
	_, statError := os.Stat(path)
	assert.ErrorIs(t, statError, os.ErrNotExist, "stats must not create the database")
}

func TestDoctorHealthyExitsZero(t *testing.T) {
	path := seededDatabase(t, time.Now().UTC().Add(-time.Hour))

	var output bytes.Buffer
	err := run([]string{"doctor", "--db", path}, &output)
	assert.NoError(t, err, output.String())
	assert.Contains(t, output.String(), "PASS  database")
}

func TestDoctorStaleWatcherExitsNonZero(t *testing.T) {
	path := seededDatabase(t, time.Now().UTC().Add(-30*time.Hour))

	var output bytes.Buffer
	err := run([]string{"doctor", "--db", path}, &output)
	assert.ErrorIs(t, err, errUnhealthy)
	assert.Contains(t, output.String(), "FAIL  watcher")
	assert.Contains(t, output.String(), "verdict: unhealthy")

	// A wider limit passes the same database.
	output.Reset()
	assert.NoError(t, run([]string{"doctor", "--db", path, "--max-watcher-age", "48h"}, &output))
}

func TestDoctorJSON(t *testing.T) {
	path := seededDatabase(t, time.Now().UTC().Add(-30*time.Hour))

	var output bytes.Buffer
	err := run([]string{"doctor", "--db", path, "--json"}, &output)
	assert.ErrorIs(t, err, errUnhealthy, "--json still exits non-zero")

	var report struct {
		Healthy bool `json:"healthy"`
		Checks  []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &report), output.String())
	assert.False(t, report.Healthy)
	require.Len(t, report.Checks, 3)
	assert.Equal(t, "watcher", report.Checks[2].Name)
	assert.Equal(t, "FAIL", report.Checks[2].Status)
}

func TestDoctorUnwritablePathExitsNonZero(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(parent, []byte("x"), 0o600))

	var output bytes.Buffer
	err := run([]string{"doctor", "--db", filepath.Join(parent, "shopper.db")}, &output)
	assert.ErrorIs(t, err, errUnhealthy)
	assert.Contains(t, output.String(), "FAIL  database")
}

// In stdio mode stdout is the JSON-RPC stream. Every line on it must be a
// protocol message, even with debug logging on.
func TestStartKeepsLogsOffStdout(t *testing.T) {
	t.Setenv("SHOPPER_LOG_LEVEL", "debug")
	path := filepath.Join(t.TempDir(), "shopper.db")

	stdinReader, stdinWriter, err := os.Pipe()
	require.NoError(t, err)
	stdoutReader, stdoutWriter, err := os.Pipe()
	require.NoError(t, err)
	stderrReader, stderrWriter, err := os.Pipe()
	require.NoError(t, err)

	originalStdin, originalStdout, originalStderr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdinReader, stdoutWriter, stderrWriter
	t.Cleanup(func() { os.Stdin, os.Stdout, os.Stderr = originalStdin, originalStdout, originalStderr })

	stderrDone := make(chan []byte)
	go func() { data, _ := io.ReadAll(stderrReader); stderrDone <- data }()

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}
	runDone := make(chan error, 1)
	go func() { runDone <- run([]string{"start", "--db", path}, stdoutWriter) }()

	for _, request := range requests {
		_, err := stdinWriter.WriteString(request + "\n")
		require.NoError(t, err)
	}

	// Wait for both responses before closing stdin, so the server is not cut off mid-reply.
	stdoutLines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(stdoutReader)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			stdoutLines <- scanner.Text()
		}
		close(stdoutLines)
	}()

	var lines []string
	timeout := time.After(10 * time.Second)
	for len(lines) < 2 {
		select {
		case line, ok := <-stdoutLines:
			require.True(t, ok, "stdout closed early")
			lines = append(lines, line)
		case <-timeout:
			t.Fatalf("no responses on stdout; got %q", lines)
		}
	}
	require.NoError(t, stdinWriter.Close())

	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("start did not exit after stdin closed")
	}
	require.NoError(t, stdoutWriter.Close())
	require.NoError(t, stderrWriter.Close())
	for line := range stdoutLines {
		lines = append(lines, line)
	}

	for _, line := range lines {
		var message map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &message), "non-JSON on stdout: %q", line)
		assert.Equal(t, "2.0", message["jsonrpc"], "non-protocol line on stdout: %q", line)
	}
	assert.Len(t, lines, 2, "one response per request")
	assert.Contains(t, strings.Join(lines, "\n"), `"list_items"`)

	stderr := string(<-stderrDone)
	assert.Contains(t, stderr, `"msg":"mcp server starting"`)
	assert.Contains(t, stderr, `"msg":"mcp server stopped"`)
}

var discardLogger = slog.New(slog.DiscardHandler)

func TestStartRefusesHTTPWithoutToken(t *testing.T) {
	t.Setenv("SHOPPER_HTTP_TOKEN", "")
	databasePath := filepath.Join(t.TempDir(), "test.db")

	err := runStart([]string{"--http", "127.0.0.1:0", "--db", databasePath}, discardLogger)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SHOPPER_HTTP_TOKEN")
	_, statErr := os.Stat(databasePath)
	assert.True(t, os.IsNotExist(statErr), "refused before touching the database")
}

func TestStartRejectsUnknownTool(t *testing.T) {
	err := runStart([]string{"--tools", "list_items,list_itmes", "--db", filepath.Join(t.TempDir(), "test.db")}, discardLogger)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "list_itmes")
}

func TestParseToolList(t *testing.T) {
	assert.Empty(t, parseToolList(""))
	assert.Equal(t, []string{"list_items", "add_item"}, parseToolList(" list_items, ,add_item,"))
}
