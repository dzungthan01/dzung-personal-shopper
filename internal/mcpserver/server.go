// Package mcpserver exposes this tool over the Model Context Protocol.
// It holds no domain logic: it only translates MCP calls to internal packages.
package mcpserver

import (
	"context"
	"fmt"

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

// New builds the MCP server and registers every tool.
func New(version string, dependencies Dependencies) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "dzung-personal-shopper",
		Version: version,
	}, nil)

	registerInspectURL(server, dependencies)
	registerAddItem(server, dependencies)
	registerListItems(server, dependencies)
	registerArchiveItem(server, dependencies)
	registerCheckItem(server, dependencies)
	registerRecordSnapshot(server, dependencies)
	registerPriceHistory(server, dependencies)

	return server
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
