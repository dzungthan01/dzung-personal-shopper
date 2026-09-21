// Package capture stores readings sent by the browser extension. The page is
// already rendered in the user's own session, so this is the only path by which
// retailers that refuse automated requests ever get a price into the database.
package capture

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/dzungthan01/dzung-personal-shopper/internal/detect"
	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/manual"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/shopify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

// confirmInterval is how stale an unchanged reading may get before it is
// recorded anyway, so the history says the price held rather than that nobody
// looked. It matches the watcher's default poll interval.
const confirmInterval = 24 * time.Hour

// Detector reports what platform a store runs, so a Shopify page captured by
// hand stays watchable instead of becoming an item only the browser can update.
type Detector interface {
	Inspect(ctx context.Context, rawURL string) (*detect.Result, error)
}

// VariantInput is one option the page offered. Prices are not carried per
// variant: the extension would have to do money arithmetic in JavaScript to
// send them, and every variant on a page shares the displayed price anyway.
type VariantInput struct {
	Name      string
	Size      string
	SKU       string
	Available bool
}

// Request is one captured page. Price and ComparePrice arrive as the decimal
// strings the page displayed, and are parsed here.
type Request struct {
	URL          string
	Title        string
	Variant      string
	Currency     string
	Price        string
	ComparePrice string
	Available    bool
	ImageURL     string
	Variants     []VariantInput
	Notes        string
}

// Result reports what the capture did, enough for the extension to show a
// confirmation without asking a second question.
type Result struct {
	ItemID        int64
	Title         string
	Variant       string
	Source        string
	Created       bool
	Archived      bool
	Recorded      bool // false when the page said nothing new
	PriceCents    int64
	PreviousCents int64
}

// Service captures pages into the wishlist.
type Service struct {
	store    *store.Store
	detector Detector
	now      func() time.Time
}

// NewService returns a Service. A nil detector means every new item is treated
// as one only the browser can read.
func NewService(database *store.Store, detector Detector) (*Service, error) {
	if database == nil {
		return nil, errors.New("capture needs a store")
	}
	return &Service{
		store:    database,
		detector: detector,
		now:      func() time.Time { return time.Now().UTC() },
	}, nil
}

// Capture records one reading, adding the item first if this is a URL and
// variant the wishlist has not seen. Capturing the same page twice is harmless,
// which is what lets the extension retry a post it could not deliver.
func (s *Service) Capture(ctx context.Context, request Request) (*Result, error) {
	productURL, err := cleanURL(request.URL)
	if err != nil {
		return nil, err
	}
	currency := strings.ToUpper(strings.TrimSpace(request.Currency))
	if currency == "" {
		return nil, errors.New("currency is required, e.g. USD")
	}

	priceCents, err := model.ParseMoney(request.Price)
	if err != nil {
		return nil, err
	}
	if priceCents <= 0 {
		return nil, fmt.Errorf("price %q is not a price anyone charges", request.Price)
	}
	var compareCents int64
	if strings.TrimSpace(request.ComparePrice) != "" {
		if compareCents, err = model.ParseMoney(request.ComparePrice); err != nil {
			return nil, fmt.Errorf("compare price: %w", err)
		}
	}

	variant := strings.TrimSpace(request.Variant)
	item, created, err := s.itemFor(ctx, productURL, variant, request)
	if err != nil {
		return nil, err
	}

	// Read the previous price before appending, so the extension can say what
	// changed the way record_snapshot does.
	previous, err := s.store.LatestObservation(ctx, item.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	observation := &model.Observation{
		ItemID:    item.ID,
		FetchedAt: s.now(),
		// The extension sees what the page charges, which is already priced for
		// the user's region: no currency switch to mistake for a price drop.
		PriceCents: priceCents,
		Currency:   currency,
		Available:  request.Available,
		Variants:   variantsOf(request.Variants, priceCents),
	}
	if compareCents > priceCents {
		observation.CompareCents = &compareCents
	}

	result := &Result{
		ItemID: item.ID, Title: item.Title, Variant: item.Variant, Source: item.Source,
		Created: created, Archived: item.Archived(), PriceCents: priceCents,
	}
	if previous != nil {
		result.PreviousCents = previous.PriceCents
	}

	// Revisiting a page that has not moved would otherwise fill the history with
	// identical rows, which is what get_price_history reads to judge a price.
	if previous != nil && !changed(previous, observation) &&
		observation.FetchedAt.Sub(previous.FetchedAt) < confirmInterval {
		return result, nil
	}

	if _, err := s.store.AddObservation(ctx, observation); err != nil {
		return nil, err
	}
	result.Recorded = true
	return result, nil
}

// changed reports whether a reading differs from the last one in any way the
// alert rules act on: the price, the was-price, stock, or which variants are
// in stock. Same price with your size back is still news.
func changed(previous, current *model.Observation) bool {
	switch {
	case previous.PriceCents != current.PriceCents:
		return true
	case previous.Available != current.Available:
		return true
	case compareOf(previous) != compareOf(current):
		return true
	}
	return inStock(previous.Variants) != inStock(current.Variants)
}

func compareOf(observation *model.Observation) int64 {
	if observation.CompareCents == nil {
		return 0
	}
	return *observation.CompareCents
}

// inStock renders the available variants, sorted so the comparison does not
// depend on the order the page happened to list them in.
func inStock(variants []model.Variant) string {
	names := make([]string, 0, len(variants))
	for _, variant := range variants {
		if variant.Available {
			names = append(names, variant.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, "\x00")
}

// itemFor returns the wishlist entry for this URL and variant, adding it if it
// is new. An archived entry is reused rather than duplicated: its price history
// is still the history of this product.
func (s *Service) itemFor(ctx context.Context, productURL, variant string, request Request) (*model.Item, bool, error) {
	existing, err := s.store.ItemByURLAndVariant(ctx, productURL, variant)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}

	item := &model.Item{
		URL:      productURL,
		Source:   s.sourceFor(ctx, productURL),
		Title:    strings.TrimSpace(request.Title),
		Variant:  variant,
		ImageURL: strings.TrimSpace(request.ImageURL),
		Notes:    strings.TrimSpace(request.Notes),
		AddedAt:  s.now(),
	}
	if item.Title == "" {
		return nil, false, errors.New("title is required for a new item")
	}
	if _, err := s.store.AddItem(ctx, item); err != nil {
		return nil, false, err
	}
	return item, true, nil
}

// sourceFor keeps a Shopify page on the source the watcher can poll, so an item
// captured once still gets checked daily. Detection failure is not a capture
// failure: losing the item costs more than tracking it manually.
func (s *Service) sourceFor(ctx context.Context, productURL string) string {
	if s.detector == nil {
		return manual.Name
	}
	detected, err := s.detector.Inspect(ctx, productURL)
	if err == nil && detected.Platform == detect.PlatformShopify {
		return shopify.Name
	}
	return manual.Name
}

// cleanURL rejects anything the extension should never send. The value is
// stored and later opened from a notification, so a javascript: or file: URL
// has no business reaching the database.
func cleanURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", errors.New("url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("url %q: %w", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("url %q is not http or https", rawURL)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("url %q has no host", rawURL)
	}
	return trimmed, nil
}

// variantsOf gives every option the page's displayed price, which is what
// record_snapshot does for hand-entered readings.
func variantsOf(inputs []VariantInput, priceCents int64) []model.Variant {
	variants := make([]model.Variant, 0, len(inputs))
	for _, input := range inputs {
		name := strings.TrimSpace(input.Name)
		if name == "" {
			continue
		}
		variants = append(variants, model.Variant{
			Name: name, Size: strings.TrimSpace(input.Size), SKU: strings.TrimSpace(input.SKU),
			Available: input.Available, PriceCents: priceCents,
		})
	}
	if len(variants) == 0 {
		return nil
	}
	return variants
}
