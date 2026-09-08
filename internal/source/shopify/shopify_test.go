package shopify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
)

// frameProductJS is trimmed from a real frame-store.com response. Size is
// option3, behind Color and inseam, which is the case a naive parser gets wrong.
const frameProductJS = `{
  "title": "L'Homme Slim -- Ridgeway",
  "vendor": "frame-denim",
  "handle": "l-homme-slim-lmh0467-ridg",
  "price": 17300,
  "compare_at_price": 24800,
  "available": true,
  "featured_image": "//cdn.shopify.com/s/files/1/2259/1799/files/LMH0467_RIDG.jpg",
  "options": [
    {"name": "Color", "position": 1},
    {"name": "Pants length type", "position": 2},
    {"name": "Size", "position": 3}
  ],
  "variants": [
    {"title": "Ridgeway / 32\" / 28", "option1": "Ridgeway", "option2": "32\"", "option3": "28",
     "sku": "LMH0467-RIDG-28", "price": 17300, "compare_at_price": 24800, "available": true},
    {"title": "Ridgeway / 32\" / 30", "option1": "Ridgeway", "option2": "32\"", "option3": "30",
     "sku": "LMH0467-RIDG-30", "price": 17300, "compare_at_price": 24800, "available": false}
  ]
}`

// newTestServer serves /meta.json and one product, and reports which paths were hit.
func newTestServer(t *testing.T, productJSON string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/meta.json":
			_, _ = w.Write([]byte(`{"currency":"USD","myshopify_domain":"frame.myshopify.com"}`))
		case "/products/l-homme-slim-lmh0467-ridg.js":
			_, _ = w.Write([]byte(productJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, &paths
}

func TestFetch(t *testing.T) {
	server, _ := newTestServer(t, frameProductJS)

	snapshot, err := New(server.Client(), "").Fetch(context.Background(),
		server.URL+"/products/l-homme-slim-lmh0467-ridg")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if snapshot.Title != "L'Homme Slim -- Ridgeway" {
		t.Errorf("Title = %q", snapshot.Title)
	}
	if snapshot.Brand != "frame-denim" {
		t.Errorf("Brand = %q, want frame-denim", snapshot.Brand)
	}
	if snapshot.PriceCents != 17300 {
		t.Errorf("PriceCents = %d, want 17300 (integer minor units, no float)", snapshot.PriceCents)
	}
	if snapshot.CompareCents != 24800 {
		t.Errorf("CompareCents = %d, want 24800", snapshot.CompareCents)
	}
	if snapshot.Currency != "USD" {
		t.Errorf("Currency = %q; it comes from /meta.json, not the product", snapshot.Currency)
	}
	if !snapshot.Available {
		t.Error("Available = false, want true")
	}
	// Protocol-relative URLs render as broken images if not fixed up.
	if snapshot.ImageURL != "https://cdn.shopify.com/s/files/1/2259/1799/files/LMH0467_RIDG.jpg" {
		t.Errorf("ImageURL = %q, want an https:-prefixed URL", snapshot.ImageURL)
	}
}

func TestFetchResolvesSizeFromOptionsNotOption1(t *testing.T) {
	server, _ := newTestServer(t, frameProductJS)

	snapshot, err := New(server.Client(), "").Fetch(context.Background(),
		server.URL+"/products/l-homme-slim-lmh0467-ridg")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if len(snapshot.Variants) != 2 {
		t.Fatalf("got %d variants, want 2", len(snapshot.Variants))
	}
	// option1 is "Ridgeway" (a colour). Reading it as the size is the bug.
	if snapshot.Variants[0].Size != "28" {
		t.Errorf("Variants[0].Size = %q, want 28; size is option3 here", snapshot.Variants[0].Size)
	}
	if snapshot.Variants[1].Size != "30" {
		t.Errorf("Variants[1].Size = %q, want 30", snapshot.Variants[1].Size)
	}
	if got := snapshot.AvailableSizes(); len(got) != 1 || got[0] != "28" {
		t.Errorf("AvailableSizes() = %v, want [28]; size 30 is out of stock", got)
	}
	if !snapshot.HasSize("28") {
		t.Error("HasSize(28) = false, want true")
	}
	if snapshot.HasSize("30") {
		t.Error("HasSize(30) = true, but that variant is unavailable")
	}
}

func TestCurrencyIsFetchedOncePerHost(t *testing.T) {
	server, paths := newTestServer(t, frameProductJS)
	shopifySource := New(server.Client(), "")
	productURL := server.URL + "/products/l-homme-slim-lmh0467-ridg"

	for i := 0; i < 3; i++ {
		if _, err := shopifySource.Fetch(context.Background(), productURL); err != nil {
			t.Fatalf("Fetch() call %d error = %v", i, err)
		}
	}

	var metaCalls int
	for _, path := range *paths {
		if path == "/meta.json" {
			metaCalls++
		}
	}
	if metaCalls != 1 {
		t.Errorf("/meta.json fetched %d times across 3 Fetch calls, want 1", metaCalls)
	}
}

func TestProductHandleExtraction(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"plain", "/products/le-high-straight", "le-high-straight"},
		{"with .js", "/products/le-high-straight.js", "le-high-straight"},
		{"with .json", "/products/le-high-straight.json", "le-high-straight"},
		{"inside a collection", "/collections/denim/products/le-high-straight", "le-high-straight"},
		{"trailing slash", "/products/le-high-straight/", "le-high-straight"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			parsedURL, err := url.Parse("https://frame-store.com" + testCase.path)
			if err != nil {
				t.Fatalf("parse url: %v", err)
			}
			got, err := productHandle(parsedURL)
			if err != nil {
				t.Fatalf("productHandle() error = %v", err)
			}
			if got != testCase.want {
				t.Errorf("handle = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestFetchRejectsNonProductURL(t *testing.T) {
	server, _ := newTestServer(t, frameProductJS)

	_, err := New(server.Client(), "").Fetch(context.Background(), server.URL+"/collections/denim")
	if !errors.Is(err, source.ErrNotAProductURL) {
		t.Errorf("error = %v, want ErrNotAProductURL", err)
	}
}

func TestFetchSurfacesStoreErrors(t *testing.T) {
	server, _ := newTestServer(t, frameProductJS)

	_, err := New(server.Client(), "").Fetch(context.Background(), server.URL+"/products/does-not-exist")
	if err == nil {
		t.Fatal("Fetch() error = nil, want a 404 to surface")
	}
}
