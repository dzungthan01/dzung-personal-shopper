// Package mcpserver exposes this tool over the Model Context Protocol.
// It holds no domain logic: it only translates MCP calls to internal packages.
package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dzungthan01/dzung-personal-shopper/internal/detect"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// Dependencies are the collaborators tool handlers need, injected so tests can supply fakes.
type Dependencies struct {
	Detector *detect.Client
	Store    *store.Store
	Sources  *source.Registry
}

// registrations maps each tool name to the function that registers it.
// TestToolNamesMatchRegistrations keeps the names in step with the tools.
var registrations = []struct {
	name     string
	register func(*mcp.Server, Dependencies)
}{
	{"inspect_url", registerInspectURL},
	{"add_item", registerAddItem},
	{"list_items", registerListItems},
	{"archive_item", registerArchiveItem},
	{"check_item", registerCheckItem},
	{"record_snapshot", registerRecordSnapshot},
	{"get_price_history", registerPriceHistory},
	{"list_alerts", registerListAlerts},
	{"ack_alerts", registerAckAlerts},
}

// ToolNames lists every tool this server can register, in registration order.
func ToolNames() []string {
	names := make([]string, 0, len(registrations))
	for _, registration := range registrations {
		names = append(names, registration.name)
	}
	return names
}

// New builds the MCP server and registers every tool.
func New(version string, dependencies Dependencies) *mcp.Server {
	server, err := NewWithTools(version, dependencies, nil)
	if err != nil {
		panic(err) // unreachable: an empty allowlist names no unknown tools
	}
	return server
}

// NewWithTools builds the MCP server with only the named tools, or every tool
// when names is empty. An unknown name is an error, so a typo is not a silent gap.
func NewWithTools(version string, dependencies Dependencies, names []string) (*mcp.Server, error) {
	known := ToolNames()
	var unknown []string
	for _, name := range names {
		if !slices.Contains(known, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown tools %s; known tools are %s",
			strings.Join(unknown, ", "), strings.Join(known, ", "))
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "dzung-personal-shopper",
		Version: version,
	}, nil)

	for _, registration := range registrations {
		if len(names) == 0 || slices.Contains(names, registration.name) {
			registration.register(server, dependencies)
		}
	}
	return server, nil
}

// inspectURLInput is the tool's argument struct. The SDK reflects over it to
// generate the JSON Schema; jsonschema tags are what the model reads.
type inspectURLInput struct {
	URL string `json:"url" jsonschema:"the product or store URL to inspect, e.g. https://frame-store.com/products/le-high-straight"`
}

// inspectURLOutput is returned to the client as structured content.
type inspectURLOutput struct {
	Host      string `json:"host" jsonschema:"the hostname that was inspected"`
	Platform  string `json:"platform" jsonschema:"the detected commerce platform: shopify, or unknown"`
	Supported bool   `json:"supported" jsonschema:"true if this tool can fetch prices from this host automatically"`
	ShopName  string `json:"shop_name,omitempty" jsonschema:"the store's own name, when it advertises one"`
	Currency  string `json:"currency,omitempty" jsonschema:"ISO currency code the store prices in, when known"`
	Reason    string `json:"reason" jsonschema:"one line explaining how the platform was determined"`
}

func registerInspectURL(server *mcp.Server, dependencies Dependencies) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "inspect_url",
		Description: "Detect which commerce platform hosts a product URL, and whether this tool " +
			"can track its price automatically. Use this before adding an item to the wishlist " +
			"to find out whether prices will update on their own or need to be recorded manually.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input inspectURLInput) (*mcp.CallToolResult, inspectURLOutput, error) {
		result, err := dependencies.Detector.Inspect(ctx, input.URL)
		if err != nil {
			// Errors mark the call failed. An unsupported host is a success, handled below.
			return nil, inspectURLOutput{}, fmt.Errorf("inspect %q: %w", input.URL, err)
		}

		output := inspectURLOutput{
			Host:      result.Host,
			Platform:  string(result.Platform),
			Supported: result.Platform != detect.PlatformUnknown,
			ShopName:  result.ShopName,
			Currency:  result.Currency,
			Reason:    result.Reason,
		}
		return nil, output, nil
	})
}
