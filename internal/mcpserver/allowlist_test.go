package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listToolNames connects a client to server and returns the names it advertises.
func listToolNames(t *testing.T, server *mcp.Server) []string {
	t.Helper()
	ctx := context.Background()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	_, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	tools, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestToolNamesMatchRegistrations(t *testing.T) {
	server := New("test", Dependencies{})

	assert.ElementsMatch(t, ToolNames(), listToolNames(t, server))
	assert.Len(t, ToolNames(), 9)
}

func TestNewWithToolsRegistersOnlyTheAllowlist(t *testing.T) {
	server, err := NewWithTools("test", Dependencies{}, []string{"list_items", "get_price_history"})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"list_items", "get_price_history"}, listToolNames(t, server))
}

func TestNewWithToolsRejectsUnknownNames(t *testing.T) {
	_, err := NewWithTools("test", Dependencies{}, []string{"list_items", "list_itmes"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "list_itmes")
}
