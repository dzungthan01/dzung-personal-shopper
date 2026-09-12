package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
)

func alertOf(kind model.AlertKind, payload model.AlertPayload) model.Alert {
	payload.Title = "Classic Easy Tote"
	payload.URL = "https://cuyana.com/products/classic-easy-tote"
	payload.Currency = "USD"
	return model.Alert{Kind: kind, Payload: payload}
}

func TestFromAlert(t *testing.T) {
	tests := []struct {
		name      string
		alert     model.Alert
		wantTitle string
		wantBody  string
	}{
		{
			name:      "price drop",
			alert:     alertOf(model.AlertPriceDrop, model.AlertPayload{PriceCents: 17300, PreviousCents: 24800}),
			wantTitle: "Price drop: Classic Easy Tote",
			wantBody:  "$248.00 → $173.00, down $75.00",
		},
		{
			name:      "price drop names the variant",
			alert:     alertOf(model.AlertPriceDrop, model.AlertPayload{Variant: "Black", PriceCents: 17300, PreviousCents: 24800}),
			wantTitle: "Price drop: Classic Easy Tote (Black)",
			wantBody:  "$248.00 → $173.00, down $75.00",
		},
		{
			name:      "sale uses the store's was-price",
			alert:     alertOf(model.AlertSaleStarted, model.AlertPayload{PriceCents: 17300, PreviousCents: 20000, CompareCents: 24800}),
			wantTitle: "On sale: Classic Easy Tote",
			wantBody:  "$173.00, was $248.00",
		},
		{
			name:      "sale without a was-price falls back to the previous price",
			alert:     alertOf(model.AlertSaleStarted, model.AlertPayload{PriceCents: 17300, PreviousCents: 24800}),
			wantTitle: "On sale: Classic Easy Tote",
			wantBody:  "$173.00, was $248.00",
		},
		{
			name:      "back in stock",
			alert:     alertOf(model.AlertBackInStock, model.AlertPayload{PriceCents: 24800}),
			wantTitle: "Back in stock: Classic Easy Tote",
			wantBody:  "Available at $248.00",
		},
		{
			name:      "variant back",
			alert:     alertOf(model.AlertVariantBack, model.AlertPayload{Variant: "Black", PriceCents: 24800}),
			wantTitle: "Back in Black: Classic Easy Tote",
			wantBody:  "Available at $248.00",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			message := FromAlert(testCase.alert)
			assert.Equal(t, testCase.wantTitle, message.Title)
			assert.Equal(t, testCase.wantBody, message.Body)
			assert.Equal(t, "https://cuyana.com/products/classic-easy-tote", message.URL)
		})
	}
}

func TestLogWritesOneLine(t *testing.T) {
	var buffer bytes.Buffer
	err := Log{Writer: &buffer}.Notify(context.Background(), Message{Title: "T", Body: "B", URL: "U"})
	require.NoError(t, err)
	assert.Equal(t, "notify: T | B | U\n", buffer.String())
}

// ntfyServer records what it receives and answers with status.
func ntfyServer(t *testing.T, status int) (*httptest.Server, *ntfyMessage, *http.Header) {
	t.Helper()
	var received ntfyMessage
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/", r.URL.Path, "JSON publishing goes to the server root")
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server, &received, &headers
}

func TestNtfyPublishesJSON(t *testing.T) {
	server, received, headers := ntfyServer(t, http.StatusOK)
	notifier, err := NewNtfy(server.URL, "shopper-7f3k9q2m", "", server.Client())
	require.NoError(t, err)

	// Non-ASCII in the title is why this is JSON and not headers.
	err = notifier.Notify(context.Background(), Message{
		Title: "Price drop: Totême Scarf", Body: "$248.00 → $173.00", URL: "https://toteme-studio.com/x",
	})
	require.NoError(t, err)

	assert.Equal(t, ntfyMessage{
		Topic: "shopper-7f3k9q2m", Title: "Price drop: Totême Scarf",
		Message: "$248.00 → $173.00", Click: "https://toteme-studio.com/x",
	}, *received)
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Empty(t, headers.Get("Authorization"), "no token configured, so none sent")
}

func TestNtfySendsTokenWhenSet(t *testing.T) {
	server, _, headers := ntfyServer(t, http.StatusOK)
	notifier, err := NewNtfy(server.URL, "shopper", "tk_secret", server.Client())
	require.NoError(t, err)

	require.NoError(t, notifier.Notify(context.Background(), Message{Body: "b"}))
	assert.Equal(t, "Bearer tk_secret", headers.Get("Authorization"))
}

func TestNtfyReportsServerErrors(t *testing.T) {
	server, _, _ := ntfyServer(t, http.StatusTooManyRequests)
	notifier, err := NewNtfy(server.URL, "shopper", "", server.Client())
	require.NoError(t, err)

	err = notifier.Notify(context.Background(), Message{Body: "b"})
	require.Error(t, err, "a failed push must be an error so the alert stays unsent")
	assert.Contains(t, err.Error(), "429")
}

func TestNewNtfy(t *testing.T) {
	_, err := NewNtfy("", "  ", "", nil)
	assert.Error(t, err, "a topic is required")

	notifier, err := NewNtfy("", "shopper", "", nil)
	require.NoError(t, err)
	assert.Equal(t, DefaultNtfyServer, notifier.serverURL)

	notifier, err = NewNtfy("https://ntfy.example.com/", "shopper", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://ntfy.example.com", notifier.serverURL, "trailing slash trimmed")
}
