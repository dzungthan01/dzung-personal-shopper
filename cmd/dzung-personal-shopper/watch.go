package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
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
	imessageTo := flagSet.String("imessage-to", os.Getenv("SHOPPER_IMESSAGE_TO"), "phone number or Apple ID to text alerts to through Messages; tried before ntfy")
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

	notifier, err := buildNotifier(*imessageTo, *ntfyServer, *ntfyTopic, *ntfyToken, logger)
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

// buildNotifier chains the configured channels, iMessage first and ntfy behind
// it. With neither configured alerts are only logged, so watch still runs with
// no setup at all.
func buildNotifier(imessageTo, ntfyServer, ntfyTopic, ntfyToken string, logger *log.Logger) (notify.Notifier, error) {
	var channels []notify.Channel

	if imessageTo != "" {
		// Fail now rather than at the first price drop a day from now.
		if _, err := exec.LookPath("osascript"); err != nil {
			return nil, fmt.Errorf("configure imessage: osascript not found, so texting needs macOS: %w", err)
		}
		imessage, err := notify.NewIMessage(imessageTo, nil)
		if err != nil {
			return nil, fmt.Errorf("configure imessage: %w", err)
		}
		channels = append(channels, notify.Channel{Name: "imessage", Notifier: imessage})
	}

	if ntfyTopic != "" {
		pusher, err := notify.NewNtfy(ntfyServer, ntfyTopic, ntfyToken, nil)
		if err != nil {
			return nil, fmt.Errorf("configure ntfy: %w", err)
		}
		channels = append(channels, notify.Channel{Name: "ntfy", Notifier: pusher})
	}

	if len(channels) == 0 {
		logger.Print("no iMessage recipient or ntfy topic set: alerts will be logged, not sent")
		return notify.Log{Writer: os.Stderr}, nil
	}

	// Names only: this line lands in a logfile, and the recipient is a phone number.
	names := make([]string, 0, len(channels))
	for _, channel := range channels {
		names = append(names, channel.Name)
	}
	logger.Printf("sending alerts via %s", strings.Join(names, ", then "))

	return notify.NewFallback(logger, channels...)
}
