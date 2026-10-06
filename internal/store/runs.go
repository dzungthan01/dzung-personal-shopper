package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxErrorSummary caps watcher_runs.error_summary, in runes. It is a hint for a
// human, not a log: the full detail belongs in the watcher's stderr.
const MaxErrorSummary = 200

// WatcherRun is one polling pass. FinishedAt is nil while the pass runs, and
// for one that died before recording its end.
type WatcherRun struct {
	ID           int64
	StartedAt    time.Time
	FinishedAt   *time.Time
	ItemsChecked int
	ItemsFailed  int
	ErrorSummary string // empty when the pass succeeded
}

// Succeeded reports whether the pass finished without an error.
func (r WatcherRun) Succeeded() bool { return r.FinishedAt != nil && r.ErrorSummary == "" }

// StartWatcherRun records that a pass began and returns its id.
func (s *Store) StartWatcherRun(ctx context.Context, startedAt time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO watcher_runs (started_at) VALUES (?) RETURNING id`,
		formatTime(startedAt),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("start watcher run: %w", err)
	}
	return id, nil
}

// FinishWatcherRun records how a pass ended. The summary is flattened to one
// line and truncated to MaxErrorSummary.
func (s *Store) FinishWatcherRun(ctx context.Context, run WatcherRun) error {
	if run.FinishedAt == nil {
		return errors.New("finish watcher run: no finish time")
	}
	var summary any
	if run.ErrorSummary != "" {
		summary = TruncateSummary(run.ErrorSummary)
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE watcher_runs SET finished_at = ?, items_checked = ?, items_failed = ?, error_summary = ? WHERE id = ?`,
		formatTime(*run.FinishedAt), run.ItemsChecked, run.ItemsFailed, summary, run.ID)
	if err != nil {
		return fmt.Errorf("finish watcher run %d: %w", run.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("watcher run %d: %w", run.ID, ErrNotFound)
	}
	return nil
}

// TruncateSummary flattens text to one line of at most MaxErrorSummary runes.
func TruncateSummary(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= MaxErrorSummary {
		return text
	}
	return string(runes[:MaxErrorSummary-1]) + "…"
}

const watcherRunColumns = `id, started_at, finished_at, items_checked, items_failed, error_summary`

// LastSuccessfulWatcherRun returns the newest pass that finished without an
// error, or ErrNotFound if there is none.
func (s *Store) LastSuccessfulWatcherRun(ctx context.Context) (*WatcherRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+watcherRunColumns+` FROM watcher_runs
		WHERE finished_at IS NOT NULL AND error_summary IS NULL
		ORDER BY started_at DESC, id DESC LIMIT 1`)
	run, err := scanWatcherRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("successful watcher run: %w", ErrNotFound)
	}
	return run, err
}

// LatestWatcherRun returns the newest pass whatever its outcome, or
// ErrNotFound if the watcher has never run.
func (s *Store) LatestWatcherRun(ctx context.Context) (*WatcherRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+watcherRunColumns+` FROM watcher_runs ORDER BY started_at DESC, id DESC LIMIT 1`)
	run, err := scanWatcherRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("latest watcher run: %w", ErrNotFound)
	}
	return run, err
}

// CountWatcherRuns counts passes started at or after since, and how many of
// them recorded an error.
func (s *Store) CountWatcherRuns(ctx context.Context, since time.Time) (runs, failures int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(error_summary) FROM watcher_runs WHERE started_at >= ?`,
		formatTime(since)).Scan(&runs, &failures)
	if err != nil {
		return 0, 0, fmt.Errorf("count watcher runs: %w", err)
	}
	return runs, failures, nil
}

func scanWatcherRun(source scanner) (*WatcherRun, error) {
	var run WatcherRun
	var startedAt string
	var finishedAt, errorSummary sql.NullString

	if err := source.Scan(&run.ID, &startedAt, &finishedAt,
		&run.ItemsChecked, &run.ItemsFailed, &errorSummary); err != nil {
		return nil, err
	}
	run.ErrorSummary = errorSummary.String

	var err error
	if run.StartedAt, err = parseTime(startedAt); err != nil {
		return nil, fmt.Errorf("watcher run %d started_at: %w", run.ID, err)
	}
	if run.FinishedAt, err = parseNullTime(finishedAt); err != nil {
		return nil, fmt.Errorf("watcher run %d finished_at: %w", run.ID, err)
	}
	return &run, nil
}
