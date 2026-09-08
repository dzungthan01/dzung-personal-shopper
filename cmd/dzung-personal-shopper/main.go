// Command dzung-personal-shopper watches a personal fashion wishlist for price drops and
// restocks, and exposes it to an LLM over the Model Context Protocol.
//
//	dzung-personal-shopper start   MCP server over stdio; runs only while a client runs it
//	dzung-personal-shopper watch   long-lived poller; added in step 6
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dzungthan01/dzung-personal-shopper/internal/detect"
	"github.com/dzungthan01/dzung-personal-shopper/internal/mcpserver"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/manual"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/shopify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "dzung-personal-shopper: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		usage()
		return fmt.Errorf("no subcommand given")
	}

	switch cmd := os.Args[1]; cmd {
	case "start":
		return runStart(os.Args[2:])
	case "migrate":
		return runMigrate(os.Args[2:])
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dzung-personal-shopper - wishlist price and stock monitor

usage:
  dzung-personal-shopper start     run the MCP server over stdio
  dzung-personal-shopper migrate   create the database and apply migrations
  dzung-personal-shopper version   print the version
`)
}

func runStart(args []string) error {
	flagSet := flag.NewFlagSet("start", flag.ContinueOnError)
	userAgent := flagSet.String("user-agent", "", "User-Agent sent to stores (identifies you to store operators)")
	databasePath := flagSet.String("db", "", "database file (default: XDG data dir)")
	if err := flagSet.Parse(args); err != nil {
		return err
	}

	// Stop cleanly on Ctrl-C or SIGTERM.
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

	server := mcpserver.New(version, mcpserver.Dependencies{
		Detector: detect.New(nil, *userAgent),
		Store:    database,
		Sources: source.NewRegistry(
			shopify.New(nil, *userAgent),
			manual.New(),
		),
	})

	fmt.Fprintf(os.Stderr, "dzung-personal-shopper %s: MCP server on stdio\n", version)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() != nil {
			return nil // shutting down on a signal is not a failure
		}
		return fmt.Errorf("start: %w", err)
	}
	return nil
}

// runMigrate creates the database and applies pending migrations. Open does this
// on every start too; this subcommand exists to do it deliberately.
func runMigrate(args []string) error {
	flagSet := flag.NewFlagSet("migrate", flag.ContinueOnError)
	databasePath := flagSet.String("db", "", "database file (default: XDG data dir)")
	if err := flagSet.Parse(args); err != nil {
		return err
	}

	path, err := resolveDatabasePath(*databasePath)
	if err != nil {
		return err
	}

	database, err := store.Open(path)
	if err != nil {
		return err
	}
	defer database.Close()

	fmt.Printf("database ready at %s\n", path)
	return nil
}

// resolveDatabasePath returns the override when set, otherwise the default location.
func resolveDatabasePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return store.DefaultPath()
}
