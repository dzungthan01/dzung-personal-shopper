package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingRunner captures one call instead of sending a real text.
type recordingRunner struct {
	ctx  context.Context
	name string
	args []string
	err  error
}

func (r *recordingRunner) run(ctx context.Context, name string, args ...string) error {
	r.ctx, r.name, r.args = ctx, name, args
	return r.err
}

func TestIMessageRunsOsascriptWithSeparateArguments(t *testing.T) {
	runner := &recordingRunner{}
	notifier, err := NewIMessage("+15551234567", runner.run)
	require.NoError(t, err)

	require.NoError(t, notifier.Notify(context.Background(), Message{
		Title: "Price drop: Classic Easy Tote",
		Body:  "$248.00 → $173.00, down $75.00",
		URL:   "https://cuyana.com/products/classic-easy-tote",
	}))

	assert.Equal(t, "osascript", runner.name)
	require.Len(t, runner.args, 4, "flag, script, recipient and text")
	assert.Equal(t, "-e", runner.args[0])
	assert.Equal(t, sendScript, runner.args[1])
	assert.Equal(t, "+15551234567", runner.args[2])
	assert.Equal(t, "Price drop: Classic Easy Tote\n$248.00 → $173.00, down $75.00\nhttps://cuyana.com/products/classic-easy-tote",
		runner.args[3], "the URL rides in the text: a text has no tap action")

	_, hasDeadline := runner.ctx.Deadline()
	assert.True(t, hasDeadline, "osascript must be bounded, or a permission prompt hangs the pass")
}

func TestIMessagePassesHostileTitleAsData(t *testing.T) {
	// A store controls the product title, so this is the payload that matters.
	hostile := `Tote \ "quoted" " & do shell script "echo pwned`
	runner := &recordingRunner{}
	notifier, err := NewIMessage("dzung@example.com", runner.run)
	require.NoError(t, err)

	require.NoError(t, notifier.Notify(context.Background(), Message{
		Title: hostile, Body: "$173.00", URL: "https://everlane.com/products/tote",
	}))

	require.Len(t, runner.args, 4)
	assert.Equal(t, hostile+"\n$173.00\nhttps://everlane.com/products/tote", runner.args[3],
		"the text reaches argv byte for byte, so AppleScript never parses it")
	assert.Equal(t, sendScript, runner.args[1], "the script is a constant, whatever the title says")
	assert.NotContains(t, runner.args[1], "pwned")
}

func TestIMessageSkipsEmptyParts(t *testing.T) {
	runner := &recordingRunner{}
	notifier, err := NewIMessage("+15551234567", runner.run)
	require.NoError(t, err)

	require.NoError(t, notifier.Notify(context.Background(), Message{Body: "Available at $248.00"}))
	require.Len(t, runner.args, 4)
	assert.Equal(t, "Available at $248.00", runner.args[3], "no blank lines for the missing parts")
}

func TestIMessageRejectsAnEmptyMessage(t *testing.T) {
	runner := &recordingRunner{}
	notifier, err := NewIMessage("+15551234567", runner.run)
	require.NoError(t, err)

	assert.Error(t, notifier.Notify(context.Background(), Message{}))
	assert.Empty(t, runner.name, "nothing ran")
}

func TestNewIMessage(t *testing.T) {
	_, err := NewIMessage("   ", nil)
	assert.Error(t, err, "a recipient is required")

	_, err = NewIMessage("-e", nil)
	assert.Error(t, err, "a recipient must not read as an osascript flag")

	notifier, err := NewIMessage("  +15551234567  ", nil)
	require.NoError(t, err)
	assert.Equal(t, "+15551234567", notifier.recipient, "trimmed")
	assert.Equal(t, defaultIMessageTimeout, notifier.timeout)
}

func TestIMessageReportsRunnerErrors(t *testing.T) {
	runner := &recordingRunner{err: errors.New("exit status 1: Not authorized to send Apple events")}
	notifier, err := NewIMessage("+15551234567", runner.run)
	require.NoError(t, err)

	err = notifier.Notify(context.Background(), Message{Body: "b"})
	require.Error(t, err, "a failed send must be an error so the alert stays unsent")
	assert.Contains(t, err.Error(), "imessage")
	assert.Contains(t, err.Error(), "Not authorized to send Apple events")
}

// The default runner is what carries osascript's reason, so exercise it on a
// command that fails the same way instead of on Messages.
func TestRunOsascriptIncludesStderr(t *testing.T) {
	err := runOsascript(context.Background(), "sh", "-c", "echo 'Messages got an error: Not authorized' >&2; exit 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1")
	assert.Contains(t, err.Error(), "Messages got an error: Not authorized")

	assert.NoError(t, runOsascript(context.Background(), "sh", "-c", "exit 0"))
}
