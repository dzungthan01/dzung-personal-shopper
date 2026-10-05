// Package health reports on the tool's own state: the checks behind doctor
// and the counts behind stats. Both read the database; neither changes it.
package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// DefaultMaxWatcherAge is how long since the last successful pass before the
// watcher counts as stale. The watcher polls daily, so one missed pass fails.
const DefaultMaxWatcherAge = 24 * time.Hour

// Status is the outcome of one check.
type Status string

const (
	Pass Status = "PASS"
	Warn Status = "WARN"
	Fail Status = "FAIL"
)

// Result is one check's outcome, with a line saying why.
type Result struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Report is every check plus a one-line verdict.
type Report struct {
	Healthy bool     `json:"healthy"` // no check failed; warnings allowed
	Verdict string   `json:"verdict"`
	Checks  []Result `json:"checks"`
}

// Options configures Check. An empty DatabasePath means the default location.
type Options struct {
	DatabasePath  string
	MaxWatcherAge time.Duration
	Now           func() time.Time
}

// Check runs every check against the database without migrating or writing
// to it, so it is safe beside a running start or watch.
func Check(ctx context.Context, options Options) Report {
	if options.MaxWatcherAge <= 0 {
		options.MaxWatcherAge = DefaultMaxWatcherAge
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}

	database, databaseResult := checkDatabase(options.DatabasePath)
	if database == nil {
		return newReport(databaseResult, notChecked("migrations"), notChecked("watcher"))
	}
	defer database.Close()

	migrationsResult, schemaCurrent := checkMigrations(ctx, database)
	watcherResult := Result{Name: "watcher", Status: Fail, Detail: "not checked: run migrate first"}
	if schemaCurrent {
		watcherResult = checkWatcher(ctx, database, options.MaxWatcherAge, options.Now())
	}
	return newReport(databaseResult, migrationsResult, watcherResult)
}

func notChecked(name string) Result {
	return Result{Name: name, Status: Fail, Detail: "not checked: the database is unavailable"}
}

func newReport(results ...Result) Report {
	var warnings, failures int
	for _, result := range results {
		switch result.Status {
		case Warn:
			warnings++
		case Fail:
			failures++
		}
	}

	report := Report{Healthy: failures == 0, Checks: results}
	switch {
	case failures > 0:
		report.Verdict = fmt.Sprintf("unhealthy: %s failed", plural(failures, "check"))
	case warnings > 0:
		report.Verdict = fmt.Sprintf("healthy, with %s", plural(warnings, "warning"))
	default:
		report.Verdict = "healthy"
	}
	return report
}

// checkDatabase confirms the file exists and that both it and its directory
// are writable: SQLite creates its -wal and -shm files beside the database.
func checkDatabase(path string) (*store.Store, Result) {
	result := Result{Name: "database", Status: Fail}
	if path == "" {
		var err error
		if path, err = store.DefaultPath(); err != nil {
			result.Detail = fmt.Sprintf("cannot resolve the database path: %v", err)
			return nil, result
		}
	}

	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		result.Detail = fmt.Sprintf("no database at %s: run migrate", path)
		return nil, result
	case err != nil:
		result.Detail = fmt.Sprintf("cannot read %s: %v", path, err)
		return nil, result
	case info.IsDir():
		result.Detail = fmt.Sprintf("%s is a directory, not a database", path)
		return nil, result
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		result.Detail = fmt.Sprintf("%s is not writable: %v", path, err)
		return nil, result
	}
	file.Close()

	probe, err := os.CreateTemp(filepath.Dir(path), ".doctor-*")
	if err != nil {
		result.Detail = fmt.Sprintf("%s is not writable, and SQLite needs it for its journal: %v", filepath.Dir(path), err)
		return nil, result
	}
	probe.Close()
	os.Remove(probe.Name())

	database, err := store.OpenExisting(path)
	if err != nil {
		result.Detail = fmt.Sprintf("cannot open %s: %v", path, err)
		return nil, result
	}
	return database, Result{Name: "database", Status: Pass, Detail: fmt.Sprintf("%s is writable", path)}
}

// checkMigrations reports whether every migration this binary carries is applied.
func checkMigrations(ctx context.Context, database *store.Store) (Result, bool) {
	current, latest, err := database.SchemaVersion(ctx)
	switch {
	case err != nil:
		return Result{Name: "migrations", Status: Fail, Detail: err.Error()}, false
	case current < latest:
		return Result{Name: "migrations", Status: Fail,
			Detail: fmt.Sprintf("schema at version %d, latest is %d: run migrate", current, latest)}, false
	case current > latest:
		return Result{Name: "migrations", Status: Warn,
			Detail: fmt.Sprintf("schema at version %d is newer than this binary's %d: upgrade it", current, latest)}, true
	default:
		return Result{Name: "migrations", Status: Pass, Detail: fmt.Sprintf("schema at version %d, up to date", current)}, true
	}
}

// checkWatcher fails when the last successful pass is older than maxAge, and
// warns when the watcher has never run, which is expected on a fresh install.
func checkWatcher(ctx context.Context, database *store.Store, maxAge time.Duration, now time.Time) Result {
	result := Result{Name: "watcher", Status: Fail}

	latest, err := database.LatestWatcherRun(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return Result{Name: "watcher", Status: Warn, Detail: "no watcher runs recorded yet: is watch running?"}
	}
	if err != nil {
		result.Detail = err.Error()
		return result
	}

	success, err := database.LastSuccessfulWatcherRun(ctx)
	if errors.Is(err, store.ErrNotFound) {
		result.Detail = "the watcher has never completed a pass" + describeFailure(latest)
		return result
	}
	if err != nil {
		result.Detail = err.Error()
		return result
	}

	age := now.Sub(*success.FinishedAt)
	if age > maxAge {
		result.Detail = fmt.Sprintf("last success %s, over the %s limit", DescribeAge(age), describeDuration(maxAge)) + describeFailure(latest)
		return result
	}
	if !latest.Succeeded() && latest.FinishedAt != nil && latest.StartedAt.After(success.StartedAt) {
		return Result{Name: "watcher", Status: Warn,
			Detail: fmt.Sprintf("last success %s", DescribeAge(age)) + describeFailure(latest)}
	}
	return Result{Name: "watcher", Status: Pass, Detail: fmt.Sprintf("last success %s", DescribeAge(age))}
}

// describeFailure is a suffix naming why the latest pass failed, if it did.
func describeFailure(latest *store.WatcherRun) string {
	if latest.ErrorSummary == "" {
		return ""
	}
	return fmt.Sprintf("; latest run failed: %s", latest.ErrorSummary)
}

// WriteText prints one line per check, then the verdict.
func (r Report) WriteText(output io.Writer) error {
	var builder strings.Builder
	for _, result := range r.Checks {
		fmt.Fprintf(&builder, "%-4s  %-10s  %s\n", result.Status, result.Name, result.Detail)
	}
	fmt.Fprintf(&builder, "verdict: %s\n", r.Verdict)
	_, err := io.WriteString(output, builder.String())
	return err
}

// DescribeAge renders how long ago something happened: minutes under an hour,
// whole hours above.
func DescribeAge(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return plural(int(age.Minutes()), "minute") + " ago"
	default:
		return plural(int(age.Hours()), "hour") + " ago"
	}
}

// describeDuration renders 24h0m0s as 24h.
func describeDuration(duration time.Duration) string {
	text := duration.String()
	text = strings.TrimSuffix(text, "0s")
	return strings.TrimSuffix(text, "0m")
}

func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
