package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
)

// Channel is one delivery route, named so a skipped one is identifiable in the
// log and in the error.
type Channel struct {
	Name     string
	Notifier Notifier
}

// Fallback tries its channels in order and stops at the first that accepts the
// message, so a working channel never sends a second copy. The alert only
// stays unsent, and is only retried, when every channel failed.
type Fallback struct {
	channels []Channel
	logger   *log.Logger
}

// NewFallback returns a notifier over channels, most preferred first.
func NewFallback(logger *log.Logger, channels ...Channel) (*Fallback, error) {
	if len(channels) == 0 {
		return nil, errors.New("fallback needs at least one channel")
	}
	for _, channel := range channels {
		if channel.Notifier == nil {
			return nil, fmt.Errorf("fallback channel %q has no notifier", channel.Name)
		}
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Fallback{channels: channels, logger: logger}, nil
}

// Notify sends through the first channel that will take it. Failures on the
// way are logged: a channel that is broken for good would otherwise show up
// only as texts that quietly stopped arriving.
func (f *Fallback) Notify(ctx context.Context, message Message) error {
	var failures []error
	for index, channel := range f.channels {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}

		err := channel.Notifier.Notify(ctx, message)
		if err == nil {
			return nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", channel.Name, err))

		if index+1 < len(f.channels) {
			f.logger.Printf("%s failed, falling back to %s: %v", channel.Name, f.channels[index+1].Name, err)
		}
	}
	return errors.Join(failures...)
}
