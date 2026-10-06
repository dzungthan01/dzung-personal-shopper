package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/detect"
	"github.com/dzungthan01/dzung-personal-shopper/internal/mcpserver"
	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/manual"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/shopify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

const testToken = "correct-horse-battery-staple"

func TestRequireBearer(t *testing.T) {
	passed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := RequireBearer(testToken, passed)

	testCases := []struct {
		name          string
		authorization string
		want          int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong", http.StatusUnauthorized},
		{"token as a prefix", "Bearer " + testToken + "x", http.StatusUnauthorized},
		{"wrong scheme", "Basic " + testToken, http.StatusUnauthorized},
		{"bare token", testToken, http.StatusUnauthorized},
		{"correct token", "Bearer " + testToken, http.StatusTeapot},
		{"lowercase scheme", "bearer " + testToken, http.StatusTeapot},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, MCPPath, nil)
			if testCase.authorization != "" {
				request.Header.Set("Authorization", testCase.authorization)
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			assert.Equal(t, testCase.want, recorder.Code)
			if testCase.want == http.StatusUnauthorized {
				assert.Equal(t, "unauthorized\n", recorder.Body.String())
				assert.NotContains(t, recorder.Body.String(), testToken)
			}
		})
	}
}

func TestRequireBearerWithEmptyTokenRejectsEverything(t *testing.T) {
	handler := RequireBearer("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	request := httptest.NewRequest(http.MethodPost, MCPPath, nil)
	request.Header.Set("Authorization", "Bearer ")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestMCPEndpointRejectsMissingToken(t *testing.T) {
	server := httptest.NewServer(Handler(Config{Server: newMCPServer(t, nil), Version: "test", Token: testToken}))
	t.Cleanup(server.Close)

	response, err := http.Post(server.URL+MCPPath, "application/json",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	require.NoError(t, err)
	defer response.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}

func TestHealthzNeedsNoTokenAndHoldsNoUserData(t *testing.T) {
	server := httptest.NewServer(Handler(Config{Server: newMCPServer(t, nil), Version: "1.2.3", Token: testToken}))
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL + "/healthz")
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "application/json", response.Header.Get("Content-Type"))
	assert.JSONEq(t, `{"status":"ok","version":"1.2.3"}`, string(body))
}

func TestLogRequestsOmitsHeadersAndQuery(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	handler := LogRequests(logger, RequireBearer(testToken, http.NotFoundHandler()))

	request := httptest.NewRequest(http.MethodPost, MCPPath+"?token="+testToken, nil)
	request.Header.Set("Authorization", "Bearer wrong-"+testToken)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	line := logged.String()
	assert.Contains(t, line, "method=POST")
	assert.Contains(t, line, "path=/mcp")
	assert.Contains(t, line, "status=401")
	assert.Contains(t, line, "duration=")
	assert.NotContains(t, line, testToken)
}

func TestServeStopsWhenContextIsCancelled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- Serve(ctx, NewServer(http.NotFoundHandler()), listener)
	}()

	cancel()

	select {
	case err := <-served:
		assert.NoError(t, err)
	case <-time.After(shutdownGrace + time.Second):
		t.Fatal("Serve did not return after the context was cancelled")
	}
}

// bearerTransport adds the token to every request, as an ElevenLabs agent's auth header would.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(request)
}

// newMCPServer builds the real tool server over a temp database, limited to tools when non-empty.
func newMCPServer(t *testing.T, tools []string) *mcp.Server {
	server, _ := newMCPServerWithStore(t, tools)
	return server
}

func newMCPServerWithStore(t *testing.T, tools []string) (*mcp.Server, *store.Store) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { database.Close() })

	server, err := mcpserver.NewWithTools("test", mcpserver.Dependencies{
		Detector: detect.New(nil, ""),
		Store:    database,
		Sources:  source.NewRegistry(shopify.New(nil, ""), manual.New()),
	}, tools)
	require.NoError(t, err)
	return server, database
}

// seedItemWithHistory stores one manual item with two readings, a drop from $128 to $98.
func seedItemWithHistory(t *testing.T, database *store.Store) int64 {
	t.Helper()
	ctx := context.Background()
	item := &model.Item{
		URL: "https://www.everlane.com/products/womens-renew-anorak", Source: "manual",
		Title: "The ReNew Anorak", Variant: "M",
	}
	_, err := database.AddItem(ctx, item)
	require.NoError(t, err)

	first := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for index, priceCents := range []int64{12800, 9800} {
		_, err := database.AddObservation(ctx, &model.Observation{
			ItemID: item.ID, FetchedAt: first.AddDate(0, 0, index),
			PriceCents: priceCents, Currency: "USD", Available: true,
			Variants: []model.Variant{{Size: "M", Available: true}},
		})
		require.NoError(t, err)
	}
	return item.ID
}

// callTool invokes a tool over the session and decodes its structured output.
func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any, target any) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	require.NoError(t, err)
	require.False(t, result.IsError, "tool %s failed: %+v", name, result.Content)
	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, target))
}

func TestStreamableHTTPEndToEnd(t *testing.T) {
	allowlist := []string{"list_items", "check_item", "get_price_history", "list_alerts", "add_item"}
	mcpServer, database := newMCPServerWithStore(t, allowlist)
	itemID := seedItemWithHistory(t, database)

	server := httptest.NewServer(Handler(Config{Server: mcpServer, Version: "test", Token: testToken}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   server.URL + MCPPath,
		HTTPClient: &http.Client{Transport: bearerTransport{token: testToken}},
	}, nil)
	require.NoError(t, err, "initialize")
	t.Cleanup(func() { session.Close() })

	assert.Equal(t, "dzung-personal-shopper", session.InitializeResult().ServerInfo.Name)

	t.Run("tools/list respects the allowlist", func(t *testing.T) {
		tools, err := session.ListTools(ctx, nil)
		require.NoError(t, err)
		names := make([]string, 0, len(tools.Tools))
		for _, tool := range tools.Tools {
			names = append(names, tool.Name)
		}
		assert.ElementsMatch(t, allowlist, names)
	})

	t.Run("list_items", func(t *testing.T) {
		var output struct {
			Count int `json:"count"`
			Items []struct {
				ItemID     int64  `json:"item_id"`
				Title      string `json:"title"`
				PriceCents int64  `json:"price_cents"`
				Currency   string `json:"currency"`
			} `json:"items"`
		}
		callTool(t, session, "list_items", map[string]any{}, &output)

		require.Equal(t, 1, output.Count)
		require.Len(t, output.Items, 1)
		assert.Equal(t, itemID, output.Items[0].ItemID)
		assert.Equal(t, "The ReNew Anorak", output.Items[0].Title)
		assert.Equal(t, int64(9800), output.Items[0].PriceCents)
		assert.Equal(t, "USD", output.Items[0].Currency)
	})

	t.Run("get_price_history", func(t *testing.T) {
		var output struct {
			ItemID      int64 `json:"item_id"`
			LowestSeen  int64 `json:"lowest_seen_cents"`
			HighestSeen int64 `json:"highest_seen_cents"`
			Readings    []struct {
				PriceCents int64 `json:"price_cents"`
			} `json:"readings"`
		}
		callTool(t, session, "get_price_history", map[string]any{"item_id": itemID}, &output)

		assert.Equal(t, itemID, output.ItemID)
		assert.Equal(t, int64(9800), output.LowestSeen)
		assert.Equal(t, int64(12800), output.HighestSeen)
		require.Len(t, output.Readings, 2)
		assert.Equal(t, int64(9800), output.Readings[0].PriceCents, "newest first")
	})

	t.Run("a tool outside the allowlist cannot be called", func(t *testing.T) {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "archive_item", Arguments: map[string]any{"item_id": itemID},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown tool "archive_item"`)

		item, err := database.ItemByID(ctx, itemID)
		require.NoError(t, err)
		assert.False(t, item.Archived())
	})
}
