package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultNtfyServer is the public ntfy instance.
const DefaultNtfyServer = "https://ntfy.sh"

// Ntfy sends messages through an ntfy server, public or self-hosted.
type Ntfy struct {
	serverURL  string
	topic      string
	token      string
	httpClient *http.Client
}

// NewNtfy returns a notifier for topic. An empty serverURL means ntfy.sh. A
// token is only needed for reserved topics or a server with accounts.
func NewNtfy(serverURL, topic, token string, httpClient *http.Client) (*Ntfy, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil, errors.New("ntfy topic is required")
	}
	if serverURL == "" {
		serverURL = DefaultNtfyServer
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Ntfy{
		serverURL:  strings.TrimRight(serverURL, "/"),
		topic:      topic,
		token:      token,
		httpClient: httpClient,
	}, nil
}

type ntfyMessage struct {
	Topic   string `json:"topic"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message"`
	Click   string `json:"click,omitempty"`
}

// Notify publishes as JSON rather than headers: headers must be ASCII, and a
// title like "Totême" or the arrow in a price drop is not.
func (n *Ntfy) Notify(ctx context.Context, message Message) error {
	body, err := json.Marshal(ntfyMessage{
		Topic: n.topic, Title: message.Title, Message: message.Body, Click: message.URL,
	})
	if err != nil {
		return fmt.Errorf("encode ntfy message: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, n.serverURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if n.token != "" {
		request.Header.Set("Authorization", "Bearer "+n.token)
	}

	response, err := n.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("ntfy: %s: %s", response.Status, strings.TrimSpace(string(detail)))
	}
	return nil
}
