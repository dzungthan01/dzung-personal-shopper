package notify

import (
	"bytes"
	"context"
	"errors"
	"log"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubNotifier counts calls and returns whatever it was given.
type stubNotifier struct {
	calls int
	err   error
}

func (s *stubNotifier) Notify(context.Context, Message) error {
	s.calls++
	return s.err
}

func TestFallbackStopsAtTheFirstSuccess(t *testing.T) {
	first, second := &stubNotifier{}, &stubNotifier{}
	notifier, err := NewFallback(nil,
		Channel{Name: "imessage", Notifier: first},
		Channel{Name: "ntfy", Notifier: second},
	)
	require.NoError(t, err)

	require.NoError(t, notifier.Notify(context.Background(), Message{Body: "b"}))
	assert.Equal(t, 1, first.calls)
	assert.Equal(t, 0, second.calls, "a delivered alert must not go out twice")
}

func TestFallbackFallsThroughOnError(t *testing.T) {
	first := &stubNotifier{err: errors.New("Not authorized to send Apple events")}
	second := &stubNotifier{}
	var buffer bytes.Buffer
	notifier, err := NewFallback(log.New(&buffer, "", 0),
		Channel{Name: "imessage", Notifier: first},
		Channel{Name: "ntfy", Notifier: second},
	)
	require.NoError(t, err)

	require.NoError(t, notifier.Notify(context.Background(), Message{Body: "b"}))
	assert.Equal(t, 1, first.calls)
	assert.Equal(t, 1, second.calls)
	assert.Contains(t, buffer.String(), "imessage failed, falling back to ntfy")
	assert.Contains(t, buffer.String(), "Not authorized to send Apple events",
		"a channel broken for good has to be visible somewhere")
}

func TestFallbackFailsWhenEveryChannelFails(t *testing.T) {
	first := &stubNotifier{err: errors.New("messages not signed in")}
	second := &stubNotifier{err: errors.New("429 Too Many Requests")}
	notifier, err := NewFallback(nil,
		Channel{Name: "imessage", Notifier: first},
		Channel{Name: "ntfy", Notifier: second},
	)
	require.NoError(t, err)

	err = notifier.Notify(context.Background(), Message{Body: "b"})
	require.Error(t, err, "the alert must stay unsent so the next pass retries it")
	assert.Contains(t, err.Error(), "imessage: messages not signed in")
	assert.Contains(t, err.Error(), "ntfy: 429 Too Many Requests")
}

func TestFallbackStopsOnCancelledContext(t *testing.T) {
	first, second := &stubNotifier{}, &stubNotifier{}
	notifier, err := NewFallback(nil,
		Channel{Name: "imessage", Notifier: first},
		Channel{Name: "ntfy", Notifier: second},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = notifier.Notify(ctx, Message{Body: "b"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, first.calls, "a shutdown does not start new sends")
}

func TestFallbackLogsNothingOnASingleChannel(t *testing.T) {
	var buffer bytes.Buffer
	only := &stubNotifier{err: errors.New("messages not signed in")}
	notifier, err := NewFallback(log.New(&buffer, "", 0), Channel{Name: "imessage", Notifier: only})
	require.NoError(t, err)

	require.Error(t, notifier.Notify(context.Background(), Message{Body: "b"}))
	assert.Empty(t, buffer.String(), "nothing to fall back to, and the watcher logs the error itself")
}

func TestNewFallback(t *testing.T) {
	_, err := NewFallback(nil)
	assert.Error(t, err, "at least one channel is required")

	_, err = NewFallback(nil, Channel{Name: "imessage"})
	assert.Error(t, err, "a channel without a notifier is a wiring bug")

	notifier, err := NewFallback(nil, Channel{Name: "ntfy", Notifier: &stubNotifier{}})
	require.NoError(t, err)
	assert.NoError(t, notifier.Notify(context.Background(), Message{Body: "b"}), "a nil logger discards")
}
