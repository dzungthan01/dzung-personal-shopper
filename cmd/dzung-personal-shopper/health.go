package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/dzungthan01/dzung-personal-shopper/internal/health"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// errUnhealthy makes doctor exit 1 once its report is printed.
var errUnhealthy = errors.New("doctor: at least one check failed")

func runStats(args []string, output io.Writer) error {
	flagSet := flag.NewFlagSet("stats", flag.ContinueOnError)
	databasePath := flagSet.String("db", "", "database file (default: XDG data dir)")
	asJSON := flagSet.Bool("json", false, "print JSON instead of text")
	if err := flagSet.Parse(args); err != nil {
		return err
	}

	path, err := resolveDatabasePath(*databasePath)
	if err != nil {
		return err
	}
	// OpenExisting, so a mistyped --db reports an error instead of creating a file.
	database, err := store.OpenExisting(path)
	if err != nil {
		return fmt.Errorf("stats: %w (run migrate to create it)", err)
	}
	defer database.Close()

	ctx := context.Background()
	current, latest, err := database.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if current < latest {
		return fmt.Errorf("stats: schema at version %d, latest is %d: run migrate", current, latest)
	}

	stats, err := health.Collect(ctx, database, path, time.Now().UTC())
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(output, stats)
	}
	return stats.WriteText(output)
}

func runDoctor(args []string, output io.Writer) error {
	flagSet := flag.NewFlagSet("doctor", flag.ContinueOnError)
	databasePath := flagSet.String("db", "", "database file (default: XDG data dir)")
	maxWatcherAge := flagSet.Duration("max-watcher-age", health.DefaultMaxWatcherAge, "fail if the last successful watcher pass is older than this")
	asJSON := flagSet.Bool("json", false, "print JSON instead of text")
	if err := flagSet.Parse(args); err != nil {
		return err
	}
	if *maxWatcherAge <= 0 {
		return fmt.Errorf("doctor: --max-watcher-age must be positive")
	}

	report := health.Check(context.Background(), health.Options{
		DatabasePath:  *databasePath,
		MaxWatcherAge: *maxWatcherAge,
	})

	var err error
	if *asJSON {
		err = writeJSON(output, report)
	} else {
		err = report.WriteText(output)
	}
	if err != nil {
		return err
	}
	if !report.Healthy {
		return errUnhealthy
	}
	return nil
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
