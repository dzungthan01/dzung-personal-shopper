// Package shopify fetches products from any Shopify storefront.
//
// It uses the unauthenticated /products/<handle>.js endpoint, which is the only
// one that carries both stock status and prices as integer minor units. The
// sibling /products/<handle>.json omits availability, and /products.json prices
// as decimal strings.
package shopify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
)

// Name identifies this source in the items table.
const Name = "shopify"

// maxBodyBytes caps a product response. Large catalogs have large payloads.
const maxBodyBytes = 4 << 20

// Source fetches from Shopify storefronts.
type Source struct {
	httpClient *http.Client
	userAgent  string

	// currencies caches host -> ISO code. The product endpoint omits currency,
	// so it comes from /meta.json, which changes essentially never.
	currencies sync.Map
}

// New returns a Source, defaulting the http client and User-Agent when nil or empty.
func New(httpClient *http.Client, userAgent string) *Source {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	if userAgent == "" {
		userAgent = "dzung-personal-shopper/0.1 (personal wishlist monitor)"
	}
	return &Source{httpClient: httpClient, userAgent: userAgent}
}

// Name returns "shopify".
func (s *Source) Name() string { return Name }

// Fetch reads the product at rawURL.
func (s *Source) Fetch(ctx context.Context, rawURL string) (*model.Snapshot, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	handle, err := productHandle(parsedURL)
	if err != nil {
		return nil, err
	}

	productURL := (&url.URL{
		Scheme: schemeOrHTTPS(parsedURL),
		Host:   parsedURL.Host,
		Path:   "/products/" + handle + ".js",
	}).String()

	var product productPayload
	if err := s.getJSON(ctx, productURL, &product); err != nil {
		return nil, fmt.Errorf("fetch product %q: %w", handle, err)
	}

	currency, err := s.currency(ctx, parsedURL)
	if err != nil {
		return nil, err
	}

	return product.toSnapshot(currency), nil
}

// currency returns the store's ISO code, fetching /meta.json once per host.
func (s *Source) currency(ctx context.Context, parsedURL *url.URL) (string, error) {
	if cached, ok := s.currencies.Load(parsedURL.Host); ok {
		return cached.(string), nil
	}

	metaURL := (&url.URL{Scheme: schemeOrHTTPS(parsedURL), Host: parsedURL.Host, Path: "/meta.json"}).String()
	var meta struct {
		Currency string `json:"currency"`
	}
	if err := s.getJSON(ctx, metaURL, &meta); err != nil {
		return "", fmt.Errorf("fetch store currency: %w", err)
	}
	if meta.Currency == "" {
		return "", fmt.Errorf("store %q advertises no currency", parsedURL.Host)
	}

	s.currencies.Store(parsedURL.Host, meta.Currency)
	return meta.Currency, nil
}

func (s *Source) getJSON(ctx context.Context, requestURL string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", s.userAgent)
	request.Header.Set("Accept", "application/json")

	response, err := s.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", requestURL, response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, into)
}

// productHandle pulls the handle out of a /products/<handle> path, tolerating
// trailing segments, extensions and query strings.
func productHandle(parsedURL *url.URL) (string, error) {
	segments := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")
	for i, segment := range segments {
		if segment == "products" && i+1 < len(segments) {
			handle := segments[i+1]
			handle = strings.TrimSuffix(strings.TrimSuffix(handle, ".js"), ".json")
			if handle != "" {
				return handle, nil
			}
		}
	}
	return "", fmt.Errorf("%q: %w", parsedURL.Path, source.ErrNotAProductURL)
}

func schemeOrHTTPS(parsedURL *url.URL) string {
	if parsedURL.Scheme == "http" {
		return "http"
	}
	return "https"
}

// productPayload is the subset of /products/<handle>.js that we use.
// Prices are already integer minor units, so no string parsing is needed.
type productPayload struct {
	Title          string           `json:"title"`
	Vendor         string           `json:"vendor"`
	Price          int64            `json:"price"`
	CompareAtPrice *int64           `json:"compare_at_price"`
	Available      bool             `json:"available"`
	FeaturedImage  json.RawMessage  `json:"featured_image"`
	Options        []optionPayload  `json:"options"`
	Variants       []variantPayload `json:"variants"`
}

type optionPayload struct {
	Name     string `json:"name"`
	Position int    `json:"position"`
}

type variantPayload struct {
	Title          string  `json:"title"`
	Option1        *string `json:"option1"`
	Option2        *string `json:"option2"`
	Option3        *string `json:"option3"`
	SKU            string  `json:"sku"`
	Price          int64   `json:"price"`
	CompareAtPrice *int64  `json:"compare_at_price"`
	Available      bool    `json:"available"`
}

func (p productPayload) toSnapshot(currency string) *model.Snapshot {
	snapshot := &model.Snapshot{
		FetchedAt:  time.Now().UTC(),
		Brand:      p.Vendor,
		Title:      p.Title,
		Currency:   currency,
		PriceCents: p.Price,
		Available:  p.Available,
		ImageURL:   absoluteImageURL(p.FeaturedImage),
	}
	if p.CompareAtPrice != nil {
		snapshot.CompareCents = *p.CompareAtPrice
	}

	position := sizePosition(p.Options)
	for _, variant := range p.Variants {
		snapshot.Variants = append(snapshot.Variants, model.Variant{
			Size:       variant.size(position),
			SKU:        variant.SKU,
			Available:  variant.Available,
			PriceCents: variant.Price,
		})
	}
	return snapshot
}

// sizePosition finds which option axis holds the size, 1-based, or 0 if none.
// Stores order their options freely: on FRAME size is option3, behind colour
// and inseam.
func sizePosition(options []optionPayload) int {
	for _, option := range options {
		if strings.Contains(strings.ToLower(option.Name), "size") {
			return option.Position
		}
	}
	if len(options) == 1 {
		return 1
	}
	return 0
}

// size returns the variant's size, falling back to its full title when the
// store has no option named "size".
func (v variantPayload) size(position int) string {
	var value *string
	switch position {
	case 1:
		value = v.Option1
	case 2:
		value = v.Option2
	case 3:
		value = v.Option3
	}
	if value != nil && *value != "" {
		return *value
	}
	return v.Title
}

// absoluteImageURL fixes Shopify's protocol-relative image URLs
// ("//cdn.shopify.com/..."). The field is null on products with no image.
func absoluteImageURL(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	return value
}
