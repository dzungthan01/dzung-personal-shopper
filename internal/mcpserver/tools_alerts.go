package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dzungthan01/dzung-personal-shopper/internal/notify"
)

type listAlertsInput struct {
	UnreadOnly bool `json:"unread_only,omitempty" jsonschema:"only alerts not yet acknowledged"`
	Limit      int  `json:"limit,omitempty" jsonschema:"how many to return, newest first; 0 means all"`
}

type alertEntry struct {
	AlertID       int64  `json:"alert_id" jsonschema:"pass to ack_alerts to mark it read"`
	ItemID        int64  `json:"item_id"`
	Kind          string `json:"kind" jsonschema:"price_drop, sale_started, back_in_stock or variant_back"`
	Summary       string `json:"summary" jsonschema:"one line, the same wording the push notification used"`
	Detail        string `json:"detail"`
	URL           string `json:"url,omitempty"`
	Variant       string `json:"variant,omitempty"`
	PriceCents    int64  `json:"price_cents,omitempty"`
	PreviousCents int64  `json:"previous_cents,omitempty"`
	Currency      string `json:"currency,omitempty"`
	CreatedAt     string `json:"created_at" jsonschema:"RFC3339"`
	Pushed        bool   `json:"pushed" jsonschema:"false when the push has not gone out yet"`
	Read          bool   `json:"read"`
}

type listAlertsOutput struct {
	Alerts []alertEntry `json:"alerts"`
	Count  int          `json:"count"`
}

func registerListAlerts(server *mcp.Server, dependencies Dependencies) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_alerts",
		Description: "List what the background watcher has found: price drops, sales, restocks. " +
			"Use unread_only to show just what the user has not acknowledged yet.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input listAlertsInput) (*mcp.CallToolResult, listAlertsOutput, error) {
		alerts, err := dependencies.Store.ListAlerts(ctx, input.UnreadOnly, input.Limit)
		if err != nil {
			return nil, listAlertsOutput{}, err
		}

		output := listAlertsOutput{Alerts: make([]alertEntry, 0, len(alerts))}
		for _, alert := range alerts {
			message := notify.FromAlert(alert)
			output.Alerts = append(output.Alerts, alertEntry{
				AlertID: alert.ID, ItemID: alert.ItemID, Kind: string(alert.Kind),
				Summary: message.Title, Detail: message.Body, URL: message.URL,
				Variant: alert.Payload.Variant, PriceCents: alert.Payload.PriceCents,
				PreviousCents: alert.Payload.PreviousCents, Currency: alert.Payload.Currency,
				CreatedAt: alert.CreatedAt.Format(rfc3339),
				Pushed:    alert.Notified(), Read: alert.ReadAt != nil,
			})
		}
		output.Count = len(output.Alerts)
		return nil, output, nil
	})
}

type ackAlertsInput struct {
	AlertIDs []int64 `json:"alert_ids" jsonschema:"ids from list_alerts"`
}

type ackAlertsOutput struct {
	Acknowledged int    `json:"acknowledged" jsonschema:"how many changed; already-read ids are not counted"`
	Message      string `json:"message"`
}

func registerAckAlerts(server *mcp.Server, dependencies Dependencies) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "ack_alerts",
		Description: "Mark alerts as read once the user has seen them, so list_alerts with " +
			"unread_only stops showing them.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input ackAlertsInput) (*mcp.CallToolResult, ackAlertsOutput, error) {
		if len(input.AlertIDs) == 0 {
			return nil, ackAlertsOutput{}, errors.New("alert_ids is required")
		}
		acknowledged, err := dependencies.Store.AckAlerts(ctx, input.AlertIDs)
		if err != nil {
			return nil, ackAlertsOutput{}, err
		}
		return nil, ackAlertsOutput{
			Acknowledged: acknowledged,
			Message:      fmt.Sprintf("Marked %d of %d alerts read.", acknowledged, len(input.AlertIDs)),
		}, nil
	})
}
