// Package detect identifies which commerce platform serves a given product URL.
package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Platform is the commerce backend serving a host.
type Platform string

const (
	PlatformUnknown Platform = "unknown"
	PlatformShopify Platform = "shopify"
)

// Result describes what we learned about a host.
type Result struct {
	Host     string   `json:"host"`
	Platform Platform `json:"platform"`

	ShopName string `json:"shop_name,omitempty"`
	Currency string `json:"currency,omitempty"`

	// Reason explains the verdict in one line, for humans reading tool output.
	Reason string `json:"reason"`
}

// maxMetaBytes caps the read: a non-Shopify host may answer with a full HTML page.
const maxMetaBytes = 64 << 10

// shopifyMeta is the subset of Shopify's /meta.json that we care about.
type shopifyMeta struct {
	Name            string `json:"name"`
	Currency        string `json:"currency"`
	MyshopifyDomain string `json:"myshopify_domain"`
}

// Client performs detection. The zero value is not usable; call New.
type Client struct {
	httpClient *http.Client
	userAgent  string
}

// New returns a Client, defaulting the http client and User-Agent when nil or empty.
func New(httpClient *http.Client, userAgent string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	if userAgent == "" {
		userAgent = "dzung-personal-shopper/0.1 (personal wishlist monitor)"
	}
	return &Client{httpClient: httpClient, userAgent: userAgent}
}

// Inspect determines which platform serves rawURL. A host that is simply not
// Shopify returns PlatformUnknown with a nil error, not a failure.
func (c *Client) Inspect(ctx context.Context, rawURL string) (*Result, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("url must be http or https, got %q", parsedURL.Scheme)
	}
	if parsedURL.Host == "" {
		return nil, fmt.Errorf("url has no host: %q", rawURL)
	}

	result := &Result{Host: parsedURL.Host, Platform: PlatformUnknown}

	meta, err := c.fetchShopifyMeta(ctx, parsedURL)
	switch {
	case err != nil:
		result.Reason = fmt.Sprintf("not detected as Shopify: %v", err)
	case meta.MyshopifyDomain == "":
		result.Reason = "/meta.json parsed but had no myshopify_domain"
	default:
		result.Platform = PlatformShopify
		result.ShopName = strings.TrimSpace(meta.Name)
		result.Currency = meta.Currency
		result.Reason = fmt.Sprintf("/meta.json advertises myshopify_domain %q", meta.MyshopifyDomain)
	}
	return result, nil
}

// fetchShopifyMeta GETs https://<host>/meta.json and decodes it.
func (c *Client) fetchShopifyMeta(ctx context.Context, parsedURL *url.URL) (*shopifyMeta, error) {
	metaURL := (&url.URL{Scheme: parsedURL.Scheme, Host: parsedURL.Host, Path: "/meta.json"}).String()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /meta.json: %s", response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxMetaBytes))
	if err != nil {
		return nil, fmt.Errorf("read /meta.json: %w", err)
	}

	var meta shopifyMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("decode /meta.json: %w", err)
	}
	return &meta, nil
}
