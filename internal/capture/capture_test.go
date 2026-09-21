package capture

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/detect"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// stubDetector answers without touching the network.
type stubDetector struct {
	platform detect.Platform
	err      error
	calls    int
}

func (s *stubDetector) Inspect(context.Context, string) (*detect.Result, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &detect.Result{Platform: s.platform, Host: "cuyana.com"}, nil
}

func newService(t *testing.T, detector Detector) (*Service, *store.Store) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "capture.db"))
	require.NoError(t, err)
	t.Cleanup(func() { database.Close() })

	service, err := NewService(database, detector)
	require.NoError(t, err)
	return service, database
}

// capturing is one realistic request, so each test varies only what it tests.
func capturing() Request {
	return Request{
		URL:       "https://cuyana.com/products/classic-easy-tote",
		Title:     "Classic Easy Tote",
		Currency:  "usd",
		Price:     "248.00",
		Available: true,
		Variants:  []VariantInput{{Name: "Black", Size: "OS", Available: true}},
	}
}

func TestCaptureAddsAShopifyItemTheWatcherCanPoll(t *testing.T) {
	service, database := newService(t, &stubDetector{platform: detect.PlatformShopify})
	ctx := context.Background()

	result, err := service.Capture(ctx, capturing())
	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.Equal(t, "shopify", result.Source, "a Shopify page stays pollable after being captured")
	assert.Equal(t, int64(24800), result.PriceCents)
	assert.Equal(t, int64(0), result.PreviousCents, "nothing was recorded before")

	observation, err := database.LatestObservation(ctx, result.ItemID)
	require.NoError(t, err)
	assert.Equal(t, int64(24800), observation.PriceCents)
	assert.Equal(t, "USD", observation.Currency, "currency is normalised")
	assert.True(t, observation.Available)
	require.Len(t, observation.Variants, 1)
	assert.Equal(t, int64(24800), observation.Variants[0].PriceCents, "variants take the displayed price")
}

func TestCaptureFallsBackToManualForBlockedStores(t *testing.T) {
	service, _ := newService(t, &stubDetector{platform: detect.PlatformUnknown})

	result, err := service.Capture(context.Background(), capturing())
	require.NoError(t, err)
	assert.Equal(t, "manual", result.Source)
}

func TestCaptureSurvivesADetectorFailure(t *testing.T) {
	service, _ := newService(t, &stubDetector{err: errors.New("network is down")})

	result, err := service.Capture(context.Background(), capturing())
	require.NoError(t, err, "losing the item costs more than tracking it by hand")
	assert.Equal(t, "manual", result.Source)
}

func TestCaptureReusesTheItemAndAppendsHistory(t *testing.T) {
	detector := &stubDetector{platform: detect.PlatformShopify}
	service, database := newService(t, detector)
	ctx := context.Background()

	first, err := service.Capture(ctx, capturing())
	require.NoError(t, err)

	cheaper := capturing()
	cheaper.Price = "173.00"
	second, err := service.Capture(ctx, cheaper)
	require.NoError(t, err)

	assert.Equal(t, first.ItemID, second.ItemID, "one entry per URL and variant")
	assert.False(t, second.Created)
	assert.Equal(t, int64(24800), second.PreviousCents, "the drop the extension reports back")
	assert.Equal(t, 1, detector.calls, "only a new item needs detecting")

	items, err := database.ListItems(ctx, false)
	require.NoError(t, err)
	assert.Len(t, items, 1)

	history, err := database.ObservationHistory(ctx, first.ItemID, 0)
	require.NoError(t, err)
	assert.Len(t, history, 2, "observations are append-only")
}

func TestCaptureTracksVariantsSeparately(t *testing.T) {
	service, database := newService(t, &stubDetector{platform: detect.PlatformShopify})
	ctx := context.Background()

	small := capturing()
	small.Variant = "S"
	large := capturing()
	large.Variant = "L"

	first, err := service.Capture(ctx, small)
	require.NoError(t, err)
	second, err := service.Capture(ctx, large)
	require.NoError(t, err)

	assert.NotEqual(t, first.ItemID, second.ItemID, "one product, two sizes, two entries")
	items, err := database.ListItems(ctx, false)
	require.NoError(t, err)
	assert.Len(t, items, 2)
}

func TestCaptureRecordsASale(t *testing.T) {
	service, database := newService(t, &stubDetector{platform: detect.PlatformShopify})
	ctx := context.Background()

	onSale := capturing()
	onSale.Price = "173.00"
	onSale.ComparePrice = "248.00"

	result, err := service.Capture(ctx, onSale)
	require.NoError(t, err)

	observation, err := database.LatestObservation(ctx, result.ItemID)
	require.NoError(t, err)
	require.NotNil(t, observation.CompareCents)
	assert.Equal(t, int64(24800), *observation.CompareCents)
	assert.True(t, observation.OnSale())
}

func TestCaptureIgnoresAComparePriceThatIsNotAWasPrice(t *testing.T) {
	service, database := newService(t, &stubDetector{platform: detect.PlatformShopify})
	ctx := context.Background()

	request := capturing()
	request.ComparePrice = "248.00" // the same as the price: not a markdown
	result, err := service.Capture(ctx, request)
	require.NoError(t, err)

	observation, err := database.LatestObservation(ctx, result.ItemID)
	require.NoError(t, err)
	assert.Nil(t, observation.CompareCents, "an equal was-price would read as a sale forever")
}

func TestCaptureReusesAnArchivedItem(t *testing.T) {
	service, database := newService(t, &stubDetector{platform: detect.PlatformShopify})
	ctx := context.Background()

	first, err := service.Capture(ctx, capturing())
	require.NoError(t, err)
	require.NoError(t, database.ArchiveItem(ctx, first.ItemID))

	second, err := service.Capture(ctx, capturing())
	require.NoError(t, err)
	assert.Equal(t, first.ItemID, second.ItemID, "its price history is still this product's history")
	assert.False(t, second.Created)
	assert.True(t, second.Archived, "so the extension can say the item is archived")
}

func TestCaptureRejectsBadRequests(t *testing.T) {
	service, _ := newService(t, &stubDetector{platform: detect.PlatformShopify})
	ctx := context.Background()

	tests := map[string]func(*Request){
		"no url":            func(r *Request) { r.URL = "  " },
		"not a web url":     func(r *Request) { r.URL = "javascript:alert(1)" },
		"no host":           func(r *Request) { r.URL = "https://" },
		"no currency":       func(r *Request) { r.Currency = "" },
		"unparseable price": func(r *Request) { r.Price = "248 USD" },
		"zero price":        func(r *Request) { r.Price = "0" },
		"no title":          func(r *Request) { r.Title = "" },
	}
	for name, breakIt := range tests {
		t.Run(name, func(t *testing.T) {
			request := capturing()
			breakIt(&request)
			_, err := service.Capture(ctx, request)
			assert.Error(t, err)
		})
	}
}

func TestNewService(t *testing.T) {
	_, err := NewService(nil, nil)
	assert.Error(t, err, "a store is required")
}

func TestCaptureSkipsAPageThatHasNotMoved(t *testing.T) {
	service, database := newService(t, &stubDetector{platform: detect.PlatformShopify})
	ctx := context.Background()

	first, err := service.Capture(ctx, capturing())
	require.NoError(t, err)
	assert.True(t, first.Recorded)

	second, err := service.Capture(ctx, capturing())
	require.NoError(t, err)
	assert.False(t, second.Recorded, "reloading a page is not a price change")
	assert.Equal(t, first.ItemID, second.ItemID)
	assert.Equal(t, int64(24800), second.PreviousCents, "the extension can still show the price")

	history, err := database.ObservationHistory(ctx, first.ItemID, 0)
	require.NoError(t, err)
	assert.Len(t, history, 1, "history stays readable")
}

func TestCaptureConfirmsAStalePrice(t *testing.T) {
	service, database := newService(t, &stubDetector{platform: detect.PlatformShopify})
	ctx := context.Background()

	first, err := service.Capture(ctx, capturing())
	require.NoError(t, err)

	// A day later the price is the same, which is itself worth recording: the
	// history should say it held, not that nobody looked.
	service.now = func() time.Time { return time.Now().UTC().Add(confirmInterval + time.Minute) }
	second, err := service.Capture(ctx, capturing())
	require.NoError(t, err)
	assert.True(t, second.Recorded)

	history, err := database.ObservationHistory(ctx, first.ItemID, 0)
	require.NoError(t, err)
	assert.Len(t, history, 2)
}

func TestCaptureRecordsStockChangesAtTheSamePrice(t *testing.T) {
	tests := map[string]func(*Request){
		"sold out":      func(r *Request) { r.Available = false; r.Variants = nil },
		"size is back":  func(r *Request) { r.Variants = append(r.Variants, VariantInput{Name: "Tan", Available: true}) },
		"size sold out": func(r *Request) { r.Variants = []VariantInput{{Name: "Black", Available: false}} },
		"went on sale":  func(r *Request) { r.ComparePrice = "298.00" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			service, database := newService(t, &stubDetector{platform: detect.PlatformShopify})
			ctx := context.Background()

			first, err := service.Capture(ctx, capturing())
			require.NoError(t, err)

			request := capturing()
			change(&request)
			second, err := service.Capture(ctx, request)
			require.NoError(t, err)
			assert.True(t, second.Recorded, "the price held but the news did not")

			history, err := database.ObservationHistory(ctx, first.ItemID, 0)
			require.NoError(t, err)
			assert.Len(t, history, 2)
		})
	}
}

func TestCaptureIgnoresVariantOrder(t *testing.T) {
	service, database := newService(t, &stubDetector{platform: detect.PlatformShopify})
	ctx := context.Background()

	both := capturing()
	both.Variants = []VariantInput{{Name: "Black", Available: true}, {Name: "Tan", Available: true}}
	first, err := service.Capture(ctx, both)
	require.NoError(t, err)

	reversed := capturing()
	reversed.Variants = []VariantInput{{Name: "Tan", Available: true}, {Name: "Black", Available: true}}
	second, err := service.Capture(ctx, reversed)
	require.NoError(t, err)
	assert.False(t, second.Recorded, "the page listing sizes in another order is not news")

	history, err := database.ObservationHistory(ctx, first.ItemID, 0)
	require.NoError(t, err)
	assert.Len(t, history, 1)
}
