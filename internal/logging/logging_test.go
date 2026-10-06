package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLevel(t *testing.T) {
	for _, testCase := range []struct {
		name string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{"DEBUG", slog.LevelDebug},
		{" warn ", slog.LevelWarn},
		{"error", slog.LevelError},
	} {
		level, err := ParseLevel(testCase.name)
		require.NoError(t, err, testCase.name)
		assert.Equal(t, testCase.want, level, testCase.name)
	}

	level, err := ParseLevel("verbose")
	assert.Error(t, err)
	assert.Equal(t, slog.LevelInfo, level, "an unknown level falls back to info")
}

func TestNewWritesJSONAtLevel(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, "warn")
	require.NoError(t, err)

	logger.Info("dropped")
	logger.Warn("item check failed", "item_id", 7)

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, lines, 1, "info is below warn")
	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))
	assert.Equal(t, "item check failed", record["msg"])
	assert.Equal(t, "WARN", record["level"])
	assert.Equal(t, float64(7), record["item_id"])
}
