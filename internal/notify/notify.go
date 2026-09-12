// Package notify delivers alerts to the user. It decides how an alert reads,
// not when to send it: the watcher owns timing and retries.
package notify

import (
	"context"
	"fmt"
	"io"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
)

// Message is one notification as a person sees it.
type Message struct {
	Title string
	Body  string
	URL   string // opened when the notification is tapped
}

// Notifier sends a message. A returned error leaves the alert unsent, so the
// watcher retries it on the next tick.
type Notifier interface {
	Notify(ctx context.Context, message Message) error
}

// FromAlert turns an alert into the words a person reads.
func FromAlert(alert model.Alert) Message {
	payload := alert.Payload
	name := payload.Title
	if payload.Variant != "" {
		name = fmt.Sprintf("%s (%s)", payload.Title, payload.Variant)
	}
	money := func(cents int64) string { return model.FormatMoney(cents, payload.Currency) }

	message := Message{URL: payload.URL}
	switch alert.Kind {
	case model.AlertPriceDrop:
		message.Title = "Price drop: " + name
		message.Body = fmt.Sprintf("%s → %s, down %s",
			money(payload.PreviousCents), money(payload.PriceCents), money(payload.DropCents()))
	case model.AlertSaleStarted:
		was := payload.CompareCents
		if was == 0 {
			was = payload.PreviousCents
		}
		message.Title = "On sale: " + name
		message.Body = fmt.Sprintf("%s, was %s", money(payload.PriceCents), money(was))
	case model.AlertBackInStock:
		message.Title = "Back in stock: " + name
		message.Body = "Available at " + money(payload.PriceCents)
	case model.AlertVariantBack:
		message.Title = fmt.Sprintf("Back in %s: %s", payload.Variant, payload.Title)
		message.Body = "Available at " + money(payload.PriceCents)
	default:
		message.Title = fmt.Sprintf("%s: %s", alert.Kind, name)
		message.Body = money(payload.PriceCents)
	}
	return message
}

// Log writes messages instead of sending them: the fallback when no push
// service is configured. Give it stderr, never stdout.
type Log struct {
	Writer io.Writer
}

// Notify writes one line per message.
func (l Log) Notify(_ context.Context, message Message) error {
	_, err := fmt.Fprintf(l.Writer, "notify: %s | %s | %s\n", message.Title, message.Body, message.URL)
	return err
}
