// Package logging builds the JSON logger every subcommand writes to stderr.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// LevelVariable is the environment variable that sets the minimum level.
const LevelVariable = "SHOPPER_LOG_LEVEL"

// New returns a JSON logger writing to output at the named level. An unknown
// level falls back to info with an error, so a typo never silences logging.
func New(output io.Writer, levelName string) (*slog.Logger, error) {
	level, err := ParseLevel(levelName)
	logger := slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}))
	return logger, err
}

// ParseLevel reads debug, info, warn or error, case-insensitively. Empty is info.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("%s=%q is not debug, info, warn or error; using info", LevelVariable, name)
	}
}
