// Package httpserver serves the MCP tools over Streamable HTTP, for clients that
// reach a server by URL rather than launching it, such as an ElevenLabs agent.
package httpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPPath is where the MCP endpoint is mounted.
const MCPPath = "/mcp"

const (
	// readHeaderTimeout bounds slow-header clients. There is deliberately no
	// WriteTimeout: a Streamable HTTP response is an SSE stream that stays open.
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute

	// sessionTimeout closes MCP sessions a client abandoned without a DELETE.
	sessionTimeout = 30 * time.Minute

	// shutdownGrace is how long in-flight calls get to finish after a signal.
	shutdownGrace = 5 * time.Second
)

// Config is what Handler needs.
type Config struct {
	Server  *mcp.Server
	Version string
	Token   string // empty serves /mcp without auth; the caller decides whether that is allowed
	Logger  *slog.Logger
}

// Handler routes /mcp, behind the bearer token, and /healthz, open.
func Handler(config Config) http.Handler {
	if config.Logger == nil {
		config.Logger = slog.New(slog.DiscardHandler)
	}
	var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return config.Server },
		&mcp.StreamableHTTPOptions{
			Logger:         config.Logger,
			SessionTimeout: sessionTimeout,
			// Tunnels (ngrok, Codespaces) arrive on loopback under a public Host
			// header. With a token that is safe; without one, keep the check.
			DisableLocalhostProtection: config.Token != "",
		},
	)
	if config.Token != "" {
		mcpHandler = RequireBearer(config.Token, mcpHandler)
	}

	mux := http.NewServeMux()
	mux.Handle(MCPPath, mcpHandler)
	mux.HandleFunc("GET /healthz", healthz(config.Version))
	return LogRequests(config.Logger, mux)
}

// healthz reports liveness only: no user data, no database access.
// TODO: back this with the health package from the observability PR once it lands.
func healthz(version string) http.HandlerFunc {
	body, _ := json.Marshal(map[string]string{"status": "ok", "version": version})
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

// RequireBearer rejects requests whose Authorization header does not carry token.
// An empty token rejects everything rather than matching an empty header.
func RequireBearer(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, got, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		if token == "" || !strings.EqualFold(scheme, "Bearer") ||
			subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// LogRequests logs method, path, status and duration. Never headers or the
// query string: either could carry the token.
func LogRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(started).Round(time.Millisecond).String(),
		)
	})
}

// statusRecorder captures the status code. Unwrap lets http.ResponseController
// reach the real writer, which the SDK needs to flush SSE events.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if !s.wroteHeader {
		s.status = status
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(data []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(data)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// NewServer returns an http.Server with this package's timeouts.
func NewServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// Serve serves on listener until ctx is cancelled, then shuts down gracefully.
// Open SSE streams never go idle, so after shutdownGrace they are cut.
func Serve(ctx context.Context, server *http.Server, listener net.Listener) error {
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()

	select {
	case err := <-serveErrors:
		return err
	case <-ctx.Done():
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		_ = server.Close()
	}
	if err := <-serveErrors; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
