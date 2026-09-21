package notify

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// defaultIMessageTimeout bounds one osascript run: Messages can sit on an
// Automation permission prompt that nobody is there to answer.
const defaultIMessageTimeout = 20 * time.Second

// sendScript takes the recipient and the text as argv, never as source. The
// text carries product titles from store pages, so interpolating it into the
// script would hand a store the ability to run AppleScript here.
const sendScript = `on run argv
	set recipientAddress to item 1 of argv
	set messageText to item 2 of argv
	tell application "Messages"
		set targetService to 1st account whose service type = iMessage
		send messageText to participant recipientAddress of targetService
	end tell
end run`

// CommandRunner runs a command to completion. A returned error must carry the
// command's stderr: that is where osascript explains what went wrong.
type CommandRunner func(ctx context.Context, name string, args ...string) error

// IMessage texts through the Messages app on this Mac, which only sends while
// the machine is awake and signed in to iMessage.
type IMessage struct {
	recipient string
	runner    CommandRunner
	timeout   time.Duration
}

// NewIMessage returns a notifier that texts recipient, a phone number or the
// email of an Apple ID. A nil runner means the real osascript.
func NewIMessage(recipient string, runner CommandRunner) (*IMessage, error) {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return nil, errors.New("imessage recipient is required")
	}
	if strings.HasPrefix(recipient, "-") {
		return nil, fmt.Errorf("imessage recipient %q would read as an osascript flag", recipient)
	}
	if runner == nil {
		runner = runOsascript
	}
	return &IMessage{recipient: recipient, runner: runner, timeout: defaultIMessageTimeout}, nil
}

// Notify sends one text. The URL goes in the body of the text, because a text
// has no tap action to hang it on the way an ntfy push does.
func (i *IMessage) Notify(ctx context.Context, message Message) error {
	text := messageText(message)
	if text == "" {
		return errors.New("imessage: nothing to send")
	}

	ctx, cancel := context.WithTimeout(ctx, i.timeout)
	defer cancel()

	if err := i.runner(ctx, "osascript", "-e", sendScript, i.recipient, text); err != nil {
		return fmt.Errorf("imessage: %w", err)
	}
	return nil
}

// messageText stacks the parts one per line, skipping the empty ones.
func messageText(message Message) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{message.Title, message.Body, message.URL} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n")
}

// runOsascript folds stderr into the error, since osascript exits 1 with the
// reason on stderr and nothing on stdout.
func runOsascript(ctx context.Context, name string, args ...string) error {
	_, err := exec.CommandContext(ctx, name, args...).Output()
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if detail := strings.TrimSpace(string(exitError.Stderr)); detail != "" {
			return fmt.Errorf("%s: %s", exitError, detail)
		}
	}
	return err
}
