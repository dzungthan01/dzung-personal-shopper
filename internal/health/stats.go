package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// Stats is what stats prints. Field names are the --json contract.
type Stats struct {
	GeneratedAt  time.Time     `json:"generated_at"`
	Database     DatabaseStats `json:"database"`
	Items        ItemStats     `json:"items"`
	Observations int           `json:"observations"`
	Alerts       AlertStats    `json:"alerts"`
	Watcher      WatcherStats  `json:"watcher"`
}

// DatabaseStats locates the database. SizeBytes includes the WAL file, which
// holds recent writes until SQLite checkpoints them.
type DatabaseStats struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

type ItemStats struct {
	Active   int `json:"active"`
	Archived int `json:"archived"`
}

// AlertStats splits alerts by whether ack_alerts has marked them read.
type AlertStats struct {
	Pending      int `json:"pending"`
	Acknowledged int `json:"acknowledged"`
}

// WatcherStats counts passes by when they started. LastSuccess is nil until a
// pass has finished without an error.
type WatcherStats struct {
	Runs24Hours           int        `json:"runs_24h"`
	Failures24Hours       int        `json:"failures_24h"`
	Runs7Days             int        `json:"runs_7d"`
	Failures7Days         int        `json:"failures_7d"`
	LastSuccess           *time.Time `json:"last_success"`
	LastSuccessAgeSeconds *int64     `json:"last_success_age_seconds"`
}

// Collect reads every count stats reports. path is only used for the file size.
func Collect(ctx context.Context, database *store.Store, path string, now time.Time) (Stats, error) {
	stats := Stats{GeneratedAt: now, Database: DatabaseStats{Path: path}}

	for _, file := range []string{path, path + "-wal"} {
		info, err := os.Stat(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Stats{}, fmt.Errorf("size of %s: %w", file, err)
		}
		stats.Database.SizeBytes += info.Size()
	}

	counts, err := database.Count(ctx)
	if err != nil {
		return Stats{}, err
	}
	stats.Items = ItemStats{Active: counts.ItemsActive, Archived: counts.ItemsArchived}
	stats.Observations = counts.Observations
	stats.Alerts = AlertStats{Pending: counts.AlertsPending, Acknowledged: counts.AlertsAcknowledged}

	watcher := &stats.Watcher
	if watcher.Runs24Hours, watcher.Failures24Hours, err = database.CountWatcherRuns(ctx, now.Add(-24*time.Hour)); err != nil {
		return Stats{}, err
	}
	if watcher.Runs7Days, watcher.Failures7Days, err = database.CountWatcherRuns(ctx, now.Add(-7*24*time.Hour)); err != nil {
		return Stats{}, err
	}

	success, err := database.LastSuccessfulWatcherRun(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return Stats{}, err
	default:
		ageSeconds := int64(now.Sub(*success.FinishedAt).Seconds())
		watcher.LastSuccess = success.FinishedAt
		watcher.LastSuccessAgeSeconds = &ageSeconds
	}
	return stats, nil
}

// WriteText prints the stats as aligned lines for a human.
func (s Stats) WriteText(output io.Writer) error {
	lastSuccess := "never"
	if s.Watcher.LastSuccess != nil {
		lastSuccess = fmt.Sprintf("%s (%s)",
			s.Watcher.LastSuccess.UTC().Format("2006-01-02 15:04 MST"),
			DescribeAge(s.GeneratedAt.Sub(*s.Watcher.LastSuccess)))
	}

	lines := [][2]string{
		{"database", fmt.Sprintf("%s (%s)", s.Database.Path, formatBytes(s.Database.SizeBytes))},
		{"items", fmt.Sprintf("%d active, %d archived", s.Items.Active, s.Items.Archived)},
		{"observations", fmt.Sprintf("%d", s.Observations)},
		{"alerts", fmt.Sprintf("%d pending, %d acknowledged", s.Alerts.Pending, s.Alerts.Acknowledged)},
		{"watcher runs", fmt.Sprintf("%d in 24h (%d failed), %d in 7d (%d failed)",
			s.Watcher.Runs24Hours, s.Watcher.Failures24Hours, s.Watcher.Runs7Days, s.Watcher.Failures7Days)},
		{"last success", lastSuccess},
	}

	var builder strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&builder, "%-13s %s\n", line[0], line[1])
	}
	_, err := io.WriteString(output, builder.String())
	return err
}

func formatBytes(size int64) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", size)
	}
}
