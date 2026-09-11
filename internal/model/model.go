// Package model holds the types shared by storage, sources and the MCP layer.
package model

import "time"

// Brand maps a brand name to the storefront that sells it.
type Brand struct {
	ID         int64
	Name       string // display name, e.g. "FRAME"
	Slug       string // lookup key, e.g. "frame"
	Domain     string // e.g. "frame-store.com"
	Platform   string // "shopify" or "unknown"
	Currency   string
	VerifiedAt *time.Time
	CreatedAt  time.Time
}

// Item is one thing on the wishlist.
type Item struct {
	ID         int64
	URL        string
	BrandID    *int64 // nil when the brand is unknown
	Source     string // which Source fetches it: "shopify" or "manual"
	Title      string
	MySize     string
	ImageURL   string
	Notes      string
	AddedAt    time.Time
	ArchivedAt *time.Time // soft delete; price history survives
}

// Archived reports whether the item has been removed from the active wishlist.
func (i Item) Archived() bool { return i.ArchivedAt != nil }

// Observation is one price check. Rows are never updated, only appended.
type Observation struct {
	ID           int64
	ItemID       int64
	FetchedAt    time.Time
	PriceCents   int64
	CompareCents *int64 // the "was" price; nil when not on sale
	Currency     string
	Available    bool
	Variants     []Variant
}

// OnSale reports whether the store is advertising a reduced price.
func (o Observation) OnSale() bool {
	return o.CompareCents != nil && *o.CompareCents > o.PriceCents
}

// Variant is one purchasable option, usually a size.
type Variant struct {
	Size       string `json:"size"`
	SKU        string `json:"sku"`
	Available  bool   `json:"available"`
	PriceCents int64  `json:"price_cents"`
}

// Snapshot is what a Source returns: a point-in-time reading of a product page.
// It carries no database identity; the store turns it into an Observation.
type Snapshot struct {
	FetchedAt    time.Time
	Brand        string
	Title        string
	Currency     string
	PriceCents   int64
	CompareCents int64 // 0 when absent
	Available    bool
	Variants     []Variant
	ImageURL     string
}

// AvailableSizes returns the sizes currently in stock.
func (s Snapshot) AvailableSizes() []string {
	var sizes []string
	for _, variant := range s.Variants {
		if variant.Available {
			sizes = append(sizes, variant.Size)
		}
	}
	return sizes
}

// HasSize reports whether the named size is in stock. Matching is exact:
// v1 does no cross-region size normalization.
func (s Snapshot) HasSize(size string) bool {
	for _, variant := range s.Variants {
		if variant.Size == size && variant.Available {
			return true
		}
	}
	return false
}

// AlertKind is what happened. Values are stored verbatim in alerts.kind.
type AlertKind string

const (
	AlertPriceDrop   AlertKind = "price_drop"
	AlertSaleStarted AlertKind = "sale_started"
	AlertBackInStock AlertKind = "back_in_stock"
	AlertMySizeBack  AlertKind = "my_size_back"
)

// Alert is one thing worth telling the user about.
type Alert struct {
	ID     int64
	ItemID int64
	Kind   AlertKind

	// DedupeKey caps repeats at one alert per item, kind, value and day.
	// The rules package builds it; the store enforces uniqueness.
	DedupeKey string

	Payload    AlertPayload
	CreatedAt  time.Time
	NotifiedAt *time.Time // nil until the push has gone out
	ReadAt     *time.Time // nil until acknowledged
}

// Notified reports whether the push has already been sent.
func (a Alert) Notified() bool { return a.NotifiedAt != nil }

// AlertPayload carries the detail a notification message is built from.
type AlertPayload struct {
	Title         string `json:"title,omitempty"`
	URL           string `json:"url,omitempty"`
	Currency      string `json:"currency,omitempty"`
	Size          string `json:"size,omitempty"`
	PriceCents    int64  `json:"price_cents,omitempty"`
	PreviousCents int64  `json:"previous_cents,omitempty"`
	CompareCents  int64  `json:"compare_cents,omitempty"`
}

// DropCents is how much the price fell, or 0 if it did not.
func (p AlertPayload) DropCents() int64 {
	if p.PreviousCents > p.PriceCents {
		return p.PreviousCents - p.PriceCents
	}
	return 0
}
