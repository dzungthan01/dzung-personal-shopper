// Package model holds the types shared by storage, sources and the MCP layer.
package model

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

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
	Variant    string // the colour/size wanted, as the store names it; "" means any
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

// Variant is one purchasable option: a size, a colour, or a combination.
type Variant struct {
	Name       string `json:"name,omitempty"` // the store's full label, e.g. "Light Pistachio / M"
	Size       string `json:"size"`
	SKU        string `json:"sku"`
	Available  bool   `json:"available"`
	PriceCents int64  `json:"price_cents"`
}

// Matches reports whether this variant is the one wanted, by its full name or
// just its size, ignoring case and a word before a number: "IT 38" matches "38".
func (v Variant) Matches(wanted string) bool {
	wanted = normaliseLabel(wanted)
	return wanted != "" && (normaliseLabel(v.Name) == wanted || normaliseLabel(v.Size) == wanted)
}

// leadingLabel only fires before a digit, so a colour like "US Navy" is untouched.
var leadingLabel = regexp.MustCompile(`^[a-z]+\s*(\d)`)

func normaliseLabel(label string) string {
	label = strings.ToLower(strings.TrimSpace(label))
	return leadingLabel.ReplaceAllString(label, "$1")
}

// Label is the variant's name, falling back to its size.
func (v Variant) Label() string {
	if v.Name != "" {
		return v.Name
	}
	return v.Size
}

// VariantInStock reports whether any variant matching wanted is available.
func VariantInStock(variants []Variant, wanted string) bool {
	for _, variant := range variants {
		if variant.Matches(wanted) && variant.Available {
			return true
		}
	}
	return false
}

// ErrUnknownVariant means the wanted variant is not one the store sells.
var ErrUnknownVariant = errors.New("unknown variant")

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

// Observation turns a snapshot into a storable reading for an item.
func (s Snapshot) Observation(itemID int64) *Observation {
	observation := &Observation{
		ItemID:     itemID,
		FetchedAt:  s.FetchedAt,
		PriceCents: s.PriceCents,
		Currency:   s.Currency,
		Available:  s.Available,
		Variants:   s.Variants,
	}
	if s.CompareCents > 0 {
		compare := s.CompareCents
		observation.CompareCents = &compare
	}
	return observation
}

// AvailableVariants returns the labels of variants currently in stock.
func (s Snapshot) AvailableVariants() []string {
	var labels []string
	for _, variant := range s.Variants {
		if variant.Available {
			labels = append(labels, variant.Label())
		}
	}
	return labels
}

// ResolveVariant checks a typed variant against the product's variants. A value
// matching exactly one is normalised to that variant's full name. A snapshot
// with no variants cannot be checked, so the text is kept as given.
func (s Snapshot) ResolveVariant(typed string) (string, error) {
	typed = strings.TrimSpace(typed)
	if typed == "" || len(s.Variants) == 0 {
		return typed, nil
	}

	var matches []Variant
	for _, variant := range s.Variants {
		if variant.Matches(typed) {
			matches = append(matches, variant)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%q is not a variant of this product; choose one of %s: %w",
			typed, s.variantLabels(), ErrUnknownVariant)
	case 1:
		return matches[0].Label(), nil
	default:
		return typed, nil // e.g. size 28 in two inseams: track any of them
	}
}

func (s Snapshot) variantLabels() string {
	labels := make([]string, 0, len(s.Variants))
	for _, variant := range s.Variants {
		labels = append(labels, fmt.Sprintf("%q", variant.Label()))
	}
	return strings.Join(labels, ", ")
}

// AlertKind is what happened. Values are stored verbatim in alerts.kind.
type AlertKind string

const (
	AlertPriceDrop   AlertKind = "price_drop"
	AlertSaleStarted AlertKind = "sale_started"
	AlertBackInStock AlertKind = "back_in_stock"
	AlertVariantBack AlertKind = "variant_back"
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
	Variant       string `json:"variant,omitempty"`
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

// FormatMoney renders minor units for people: 17300, "USD" -> "$173.00".
func FormatMoney(cents int64, currency string) string {
	symbol := map[string]string{"USD": "$", "EUR": "€", "GBP": "£"}[strings.ToUpper(currency)]
	if symbol == "" {
		return fmt.Sprintf("%d.%02d %s", cents/100, cents%100, strings.ToUpper(currency))
	}
	return fmt.Sprintf("%s%d.%02d", symbol, cents/100, cents%100)
}
