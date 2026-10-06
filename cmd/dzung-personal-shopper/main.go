// Command dzung-personal-shopper watches a personal fashion wishlist for price drops and
// restocks, and exposes it to an LLM over the Model Context Protocol.
//
//	dzung-personal-shopper start   MCP server over stdio; runs only while a client runs it
//	dzung-personal-shopper start --http :8080   the same tools over Streamable HTTP
//	dzung-personal-shopper watch   long-lived poller
//	dzung-personal-shopper stats   counts, and how the watcher has been doing
//	dzung-personal-shopper doctor  health checks; exits 1 if any fails
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dzungthan01/dzung-personal-shopper/internal/detect"
	"github.com/dzungthan01/dzung-personal-shopper/internal/httpserver"
	"github.com/dzungthan01/dzung-personal-shopper/internal/logging"
	"github.com/dzungthan01/dzung-personal-shopper/internal/mcpserver"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/manual"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/shopify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "dzung-personal-shopper: %v\n", err)
		os.Exit(1)
	}
}

// run dispatches a subcommand. Reports go to output; logs always go to stderr,
// because in start, stdout carries MCP's JSON-RPC stream.
func run(args []string, output io.Writer) error {
	if len(args) < 1 {
		usage()
		return fmt.Errorf("no subcommand given")
	}

	logger, err := logging.New(os.Stderr, os.Getenv(logging.LevelVariable))
	if err != nil {
		logger.Warn("bad log level", "error", err)
	}
	slog.SetDefault(logger)

	switch command := args[0]; command {
	case "start":
		return runStart(args[1:], logger)
	case "watch":
		return runWatch(args[1:], logger)
	case "migrate":
		return runMigrate(args[1:])
	case "stats":
		return runStats(args[1:], output)
	case "doctor":
		return runDoctor(args[1:], output)
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", command)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dzung-personal-shopper - wishlist price and stock monitor

usage:
  dzung-personal-shopper start     run the MCP server over stdio (--http :8080 for Streamable HTTP)
  dzung-personal-shopper watch     poll tracked items and send alerts
  dzung-personal-shopper migrate   create the database and apply migrations
  dzung-personal-shopper stats     print counts and watcher run history
  dzung-personal-shopper doctor    check the database, schema and watcher
  dzung-personal-shopper version   print the version
`)
}

func runStart(args []string, logger *slog.Logger) error {
	flagSet := flag.NewFlagSet("start", flag.ContinueOnError)
	userAgent := flagSet.String("user-agent", "", "User-Agent sent to stores (identifies you to store operators)")
	databasePath := flagSet.String("db", "", "database file (default: XDG data dir)")
	httpAddress := flagSet.String("http", os.Getenv("SHOPPER_HTTP_ADDR"), "serve Streamable HTTP on this address, e.g. :8080, instead of stdio")
	insecureNoAuth := flagSet.Bool("insecure-no-auth", false, "allow --http without SHOPPER_HTTP_TOKEN; local testing only")
	tools := flagSet.String("tools", os.Getenv("SHOPPER_TOOLS"), "comma-separated tools to register (default: all)")
	if err := flagSet.Parse(args); err != nil {
		return err
	}

	// The token comes from the environment only: a flag would show up in ps.
	token := os.Getenv("SHOPPER_HTTP_TOKEN")
	if *httpAddress != "" && token == "" && !*insecureNoAuth {
		return fmt.Errorf("start: --http needs SHOPPER_HTTP_TOKEN set; pass --insecure-no-auth to run without one, locally only")
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

	server, err := mcpserver.NewWithTools(version, mcpserver.Dependencies{
		Detector: detect.New(nil, *userAgent),
		Store:    database,
		Sources: source.NewRegistry(
			shopify.New(nil, *userAgent),
			manual.New(),
		),
	}, parseToolList(*tools))
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}

	if *httpAddress != "" {
		return serveHTTP(ctx, *httpAddress, server, token, logger)
	}

	logger.Info("mcp server starting", "version", version, "transport", "stdio", "database", path)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() != nil {
			logger.Info("mcp server stopped", "reason", "signal")
			return nil // shutting down on a signal is not a failure
		}
		logger.Error("mcp server stopped", "error", err)
		return fmt.Errorf("start: %w", err)
	}
	logger.Info("mcp server stopped", "reason", "client disconnected")
	return nil
}

// serveHTTP serves the MCP server over Streamable HTTP until ctx is cancelled.
func serveHTTP(ctx context.Context, address string, server *mcp.Server, token string, logger *slog.Logger) error {
	if token == "" {
		logger.Warn("SERVING WITHOUT AUTHENTICATION: anyone who can reach this port can read and change the wishlist; never expose it publicly")
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	logger.Info("MCP server on Streamable HTTP", "version", version,
		"address", listener.Addr().String(), "path", httpserver.MCPPath)

	handler := httpserver.Handler(httpserver.Config{
		Server: server, Version: version, Token: token, Logger: logger,
	})
	if err := httpserver.Serve(ctx, httpserver.NewServer(handler), listener); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	logger.Info("HTTP server stopped")
	return nil
}

// parseToolList splits a comma-separated tool list, dropping blanks.
func parseToolList(list string) []string {
	var names []string
	for _, name := range strings.Split(list, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
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
