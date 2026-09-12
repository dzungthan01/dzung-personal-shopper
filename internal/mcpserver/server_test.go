package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/detect"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/manual"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/shopify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// frameProductJS mirrors a real frame-store.com response, on sale, with size as
// option3 behind Color and inseam.
const frameProductJS = `{
  "title": "L'Homme Slim -- Ridgeway", "vendor": "FRAME",
  "price": 17300, "compare_at_price": 24800, "available": true,
  "featured_image": "//cdn.shopify.com/x.jpg",
  "options": [{"name":"Color","position":1},{"name":"Pants length type","position":2},{"name":"Size","position":3}],
  "variants": [
    {"title":"Ridgeway / 32\" / 28","option1":"Ridgeway","option2":"32\"","option3":"28","sku":"A-28","price":17300,"available":true},
    {"title":"Ridgeway / 32\" / 30","option1":"Ridgeway","option2":"32\"","option3":"30","sku":"A-30","price":17300,"available":false}
  ]
}`

// storefront serves a Shopify-shaped store whose product price can be changed
// mid-test, so a price drop can be observed.
type storefront struct {
	*httptest.Server
	productJSON string
}

func newStorefront(t *testing.T) *storefront {
	t.Helper()
	front := &storefront{productJSON: frameProductJS}
	front.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/meta.json":
			_, _ = w.Write([]byte(`{"currency":"USD","name":"FRAME","myshopify_domain":"frame.myshopify.com"}`))
		case "/products/le-high.js":
			_, _ = w.Write([]byte(front.productJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(front.Close)
	return front
}

// blockedStore mimics a retailer behind bot protection: every request is a 403.
func newBlockedStore(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	return server
}

// connect wires a real client to a real server over an in-memory transport, so
// tests exercise the same path Claude does, schemas included.
func connect(t *testing.T) *mcp.ClientSession {
	session, _ := connectWithStore(t)
	return session
}

// connectWithStore also hands back the database, for tests that seed it directly.
func connectWithStore(t *testing.T) (*mcp.ClientSession, *store.Store) {
	t.Helper()
	ctx := context.Background()

	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { database.Close() })

	server := New("test", Dependencies{
		Detector: detect.New(nil, ""),
		Store:    database,
		Sources:  source.NewRegistry(shopify.New(nil, ""), manual.New()),
	})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session, database
}

// call invokes a tool and decodes its structured output into target.
func call(t *testing.T, session *mcp.ClientSession, name string, arguments, target any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("CallTool(%s) transport error = %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) returned an error result: %+v", name, result.Content)
	}
	if target != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatalf("re-encode %s output: %v", name, err)
		}
		if err := json.Unmarshal(encoded, target); err != nil {
			t.Fatalf("decode %s output: %v", name, err)
		}
	}
	return result
}

func TestAllToolsAreRegistered(t *testing.T) {
	session := connect(t)

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}

	registered := map[string]bool{}
	for _, tool := range tools.Tools {
		registered[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q has no description; the model reads it to decide when to call", tool.Name)
		}
	}
	for _, want := range []string{"inspect_url", "add_item", "list_items", "archive_item",
		"check_item", "record_snapshot", "get_price_history"} {
		if !registered[want] {
			t.Errorf("tool %q is not registered", want)
		}
	}
}

func TestAddItemFromReadableStore(t *testing.T) {
	front := newStorefront(t)
	session := connect(t)

	var added addItemOutput
	call(t, session, "add_item", map[string]any{
		"url": front.URL + "/products/le-high", "variant": "28",
	}, &added)

	if !added.AutoTracked {
		t.Error("AutoTracked = false, want true for a Shopify store")
	}
	if added.Source != shopify.Name {
		t.Errorf("Source = %q, want shopify", added.Source)
	}
	if added.PriceCents != 17300 {
		t.Errorf("PriceCents = %d, want 17300", added.PriceCents)
	}
	if added.CompareCents != 24800 {
		t.Errorf("CompareCents = %d, want 24800", added.CompareCents)
	}
	if added.Currency != "USD" {
		t.Errorf("Currency = %q, want USD from /meta.json", added.Currency)
	}
	// Size 30 is out of stock; only 28 should be listed.
	if len(added.AvailableVariants) != 1 || added.AvailableVariants[0] != `Ridgeway / 32" / 28` {
		t.Errorf("AvailableVariants = %v, want only the size 28 variant", added.AvailableVariants)
	}
	// A bare size matching one variant is stored under the store's full name.
	if added.Variant != `Ridgeway / 32" / 28` {
		t.Errorf("Variant = %q, want the full variant name", added.Variant)
	}
}

func TestAddItemFromBlockedStoreFallsBackToManual(t *testing.T) {
	blocked := newBlockedStore(t)
	session := connect(t)

	var added addItemOutput
	call(t, session, "add_item", map[string]any{
		"url": blocked.URL + "/en-us/product/some-coat", "title": "Some Coat",
	}, &added)

	// A 403 must not fail the call: the item is still tracked, by hand.
	if added.Source != manual.Name {
		t.Errorf("Source = %q, want manual for a store that returns 403", added.Source)
	}
	if added.AutoTracked {
		t.Error("AutoTracked = true, want false")
	}
	if added.PriceCents != 0 {
		t.Errorf("PriceCents = %d, want 0; nothing could be read", added.PriceCents)
	}
	if added.Title != "Some Coat" {
		t.Errorf("Title = %q, want the supplied title", added.Title)
	}
	if added.Message == "" {
		t.Error("Message is empty; the user needs to be told to use record_snapshot")
	}
}

func TestAddItemIsIdempotent(t *testing.T) {
	front := newStorefront(t)
	session := connect(t)
	arguments := map[string]any{"url": front.URL + "/products/le-high"}

	var first, second addItemOutput
	call(t, session, "add_item", arguments, &first)
	call(t, session, "add_item", arguments, &second)

	if !second.AlreadyTracked {
		t.Error("AlreadyTracked = false on a repeat add")
	}
	if second.ItemID != first.ItemID {
		t.Errorf("second add created item %d, want the existing %d", second.ItemID, first.ItemID)
	}

	var listed listItemsOutput
	call(t, session, "list_items", map[string]any{}, &listed)
	if listed.Count != 1 {
		t.Errorf("wishlist has %d items after adding the same URL twice, want 1", listed.Count)
	}
}

func TestCheckItemDetectsPriceDrop(t *testing.T) {
	front := newStorefront(t)
	session := connect(t)

	var added addItemOutput
	call(t, session, "add_item", map[string]any{
		"url": front.URL + "/products/le-high", "variant": "28",
	}, &added)

	// The store marks it down between checks.
	front.productJSON = `{"title":"L'Homme Slim -- Ridgeway","vendor":"FRAME","price":12000,
		"compare_at_price":24800,"available":true,
		"options":[{"name":"Size","position":1}],
		"variants":[{"title":"28","option1":"28","sku":"A-28","price":12000,"available":true}]}`

	var checked checkItemOutput
	call(t, session, "check_item", map[string]any{"item_id": added.ItemID}, &checked)

	if checked.PriceCents != 12000 {
		t.Errorf("PriceCents = %d, want 12000", checked.PriceCents)
	}
	if checked.PreviousCents != 17300 {
		t.Errorf("PreviousCents = %d, want 17300", checked.PreviousCents)
	}
	if checked.ChangeCents != -5300 {
		t.Errorf("ChangeCents = %d, want -5300 (negative means a drop)", checked.ChangeCents)
	}

	// Both readings must survive: observations are append-only.
	var history priceHistoryOutput
	call(t, session, "get_price_history", map[string]any{"item_id": added.ItemID}, &history)
	if len(history.Readings) != 2 {
		t.Fatalf("history has %d readings, want 2", len(history.Readings))
	}
	if history.LowestSeen != 12000 || history.HighestSeen != 17300 {
		t.Errorf("LowestSeen/HighestSeen = %d/%d, want 12000/17300", history.LowestSeen, history.HighestSeen)
	}
}

func TestRecordSnapshotTracksBlockedStore(t *testing.T) {
	blocked := newBlockedStore(t)
	session := connect(t)

	var added addItemOutput
	call(t, session, "add_item", map[string]any{
		"url": blocked.URL + "/product/coat", "title": "Wool Coat", "variant": "IT 38",
	}, &added)

	var recorded recordSnapshotOutput
	call(t, session, "record_snapshot", map[string]any{
		"item_id": added.ItemID, "price_cents": 89000, "currency": "usd",
		"available": true, "available_variants": []string{"IT 38", "IT 40"},
	}, &recorded)

	if recorded.PriceCents != 89000 {
		t.Errorf("PriceCents = %d, want 89000", recorded.PriceCents)
	}

	var listed listItemsOutput
	call(t, session, "list_items", map[string]any{}, &listed)
	entry := listed.Items[0]
	if entry.PriceCents != 89000 {
		t.Errorf("wishlist price = %d, want the hand-recorded 89000", entry.PriceCents)
	}
	if entry.Currency != "USD" {
		t.Errorf("Currency = %q, want it normalized to upper case", entry.Currency)
	}
	if entry.VariantInStock == nil || !*entry.VariantInStock {
		t.Error("VariantInStock = false/nil, want true; IT 38 was recorded as available")
	}
}

func TestArchiveItemKeepsHistory(t *testing.T) {
	front := newStorefront(t)
	session := connect(t)

	var added addItemOutput
	call(t, session, "add_item", map[string]any{"url": front.URL + "/products/le-high"}, &added)
	call(t, session, "archive_item", map[string]any{"item_id": added.ItemID}, nil)

	var active listItemsOutput
	call(t, session, "list_items", map[string]any{}, &active)
	if active.Count != 0 {
		t.Errorf("archived item still active: %d", active.Count)
	}

	var all listItemsOutput
	call(t, session, "list_items", map[string]any{"include_archived": true}, &all)
	if all.Count != 1 {
		t.Errorf("archived item missing from include_archived listing: %d", all.Count)
	}

	var history priceHistoryOutput
	call(t, session, "get_price_history", map[string]any{"item_id": added.ItemID}, &history)
	if len(history.Readings) != 1 {
		t.Errorf("archiving destroyed price history: %d readings", len(history.Readings))
	}
}

func TestRecordSnapshotRejectsBadInput(t *testing.T) {
	front := newStorefront(t)
	session := connect(t)

	var added addItemOutput
	call(t, session, "add_item", map[string]any{"url": front.URL + "/products/le-high"}, &added)

	for _, testCase := range []struct {
		name      string
		arguments map[string]any
	}{
		{"zero price", map[string]any{"item_id": added.ItemID, "price_cents": 0, "currency": "USD", "available": true}},
		{"negative price", map[string]any{"item_id": added.ItemID, "price_cents": -100, "currency": "USD", "available": true}},
		{"missing currency", map[string]any{"item_id": added.ItemID, "price_cents": 100, "currency": "", "available": true}},
		{"unknown item", map[string]any{"item_id": 9999, "price_cents": 100, "currency": "USD", "available": true}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(),
				&mcp.CallToolParams{Name: "record_snapshot", Arguments: testCase.arguments})
			if err == nil && !result.IsError {
				t.Error("call succeeded, want an error result")
			}
		})
	}
}

func TestAddItemTracksOneProductInTwoVariants(t *testing.T) {
	front := newStorefront(t)
	session := connect(t)
	productURL := front.URL + "/products/le-high"

	var size28, size30, again addItemOutput
	call(t, session, "add_item", map[string]any{"url": productURL, "variant": "28"}, &size28)
	call(t, session, "add_item", map[string]any{"url": productURL, "variant": "30"}, &size30)
	assert.NotEqual(t, size28.ItemID, size30.ItemID, "each variant is its own entry")

	call(t, session, "add_item", map[string]any{"url": productURL, "variant": "28"}, &again)
	assert.True(t, again.AlreadyTracked)
	assert.Equal(t, size28.ItemID, again.ItemID)

	// A typed bare size is saved under the full name, so the full name is a duplicate too.
	var fullName addItemOutput
	call(t, session, "add_item", map[string]any{"url": productURL, "variant": `Ridgeway / 32" / 28`}, &fullName)
	assert.True(t, fullName.AlreadyTracked, "typed 28 and its full name are the same variant")
	assert.Equal(t, size28.ItemID, fullName.ItemID)

	var listed listItemsOutput
	call(t, session, "list_items", map[string]any{}, &listed)
	assert.Equal(t, 2, listed.Count)
}

func TestAddItemRejectsUnknownVariant(t *testing.T) {
	front := newStorefront(t)
	session := connect(t)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "add_item", Arguments: map[string]any{"url": front.URL + "/products/le-high", "variant": "99"},
	})
	require.NoError(t, err)
	require.True(t, result.IsError, "an unknown variant must fail so the model can retry")

	text := result.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `Ridgeway / 32\" / 28`, "the error lists the variants that exist")

	var listed listItemsOutput
	call(t, session, "list_items", map[string]any{}, &listed)
	assert.Zero(t, listed.Count, "nothing is added when the variant is rejected")
}
