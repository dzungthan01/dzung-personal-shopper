package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dzungthan01/dzung-personal-shopper/internal/notify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/manual"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/shopify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
	"github.com/dzungthan01/dzung-personal-shopper/internal/watcher"
)

func runWatch(args []string) error {
	flagSet := flag.NewFlagSet("watch", flag.ContinueOnError)
	databasePath := flagSet.String("db", "", "database file (default: XDG data dir)")
	interval := flagSet.Duration("interval", 24*time.Hour, "how often to check every item")
	jitter := flagSet.Duration("jitter", 30*time.Minute, "random delay before the first pass, so stores are not hit on the hour")
	once := flagSet.Bool("once", false, "run a single pass and exit")
	userAgent := flagSet.String("user-agent", "", "User-Agent sent to stores")
	ntfyServer := flagSet.String("ntfy-server", os.Getenv("SHOPPER_NTFY_SERVER"), "ntfy server (default https://ntfy.sh)")
	ntfyTopic := flagSet.String("ntfy-topic", os.Getenv("SHOPPER_NTFY_TOPIC"), "ntfy topic to publish to; without one, alerts are only logged")
	ntfyToken := flagSet.String("ntfy-token", os.Getenv("SHOPPER_NTFY_TOKEN"), "ntfy access token, for reserved topics or a private server")
	if err := flagSet.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	path, err := resolveDatabasePath(*databasePath)
	if err != nil {
		return err
	}
	database, err := store.Open(path)
	if err != nil {
		return err
	}
	defer database.Close()

	logger := log.New(os.Stderr, "watch: ", log.LstdFlags)

	notifier, err := buildNotifier(*ntfyServer, *ntfyTopic, *ntfyToken, logger)
	if err != nil {
		return err
	}

	poller, err := watcher.New(watcher.Config{
		Store:    database,
		Sources:  source.NewRegistry(shopify.New(nil, *userAgent), manual.New()),
		Notifier: notifier,
		Logger:   logger,
	})
	if err != nil {
		return err
	}
	if *once {
		result, err := poller.RunOnce(ctx)
		if err != nil {
			return err
		}
		logger.Printf("pass done: %+v", result)
		return nil
	}

	logger.Printf("watching every %s, database %s", *interval, path)
	return poller.Run(ctx, *interval, *jitter)
}

// buildNotifier returns an ntfy notifier when a topic is configured, and
// otherwise one that only logs, so watch still runs with no setup.
func buildNotifier(server, topic, token string, logger *log.Logger) (notify.Notifier, error) {
	if topic == "" {
		logger.Print("no ntfy topic set: alerts will be logged, not pushed")
		return notify.Log{Writer: os.Stderr}, nil
	}
	notifier, err := notify.NewNtfy(server, topic, token, nil)
	if err != nil {
		return nil, fmt.Errorf("configure ntfy: %w", err)
	}
	return notifier, nil
}
