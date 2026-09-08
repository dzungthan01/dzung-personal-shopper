package detect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInspect covers the detection decision table against a local httptest
// server, so the suite never touches the network.
func TestInspect(t *testing.T) {
	t.Parallel()

	const shopifyMetaJSON = `{"id":1,"name":"Everlane ","currency":"USD","myshopify_domain":"everlane.myshopify.com"}`

	tests := []struct {
		name         string
		status       int
		body         string
		contentType  string
		wantPlatform Platform
		wantCurrency string
		wantShopName string
	}{
		{
			name:         "shopify store",
			status:       http.StatusOK,
			body:         shopifyMetaJSON,
			wantPlatform: PlatformShopify,
			wantCurrency: "USD",
			wantShopName: "Everlane", // trailing space in the payload is trimmed
		},
		{
			name:         "bot protection blocks us",
			status:       http.StatusForbidden,
			body:         "denied",
			wantPlatform: PlatformUnknown,
		},
		{
			name:         "host has no meta.json",
			status:       http.StatusNotFound,
			body:         "not found",
			wantPlatform: PlatformUnknown,
		},
		{
			name:         "html page served at meta.json",
			status:       http.StatusOK,
			body:         "<!doctype html><html><body>hello</body></html>",
			contentType:  "text/html",
			wantPlatform: PlatformUnknown,
		},
		{
			// Sezane really does serve this; valid JSON is not proof of Shopify.
			name:         "valid json but not shopify",
			status:       http.StatusOK,
			body:         `{"name":"Some Store","currency":"EUR"}`,
			wantPlatform: PlatformUnknown,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/meta.json" {
					t.Errorf("requested %q, want /meta.json", r.URL.Path)
				}
				if userAgent := r.Header.Get("User-Agent"); !strings.Contains(userAgent, "dzung-personal-shopper") {
					t.Errorf("User-Agent = %q, want it to identify this tool", userAgent)
				}
				if testCase.contentType != "" {
					w.Header().Set("Content-Type", testCase.contentType)
				}
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			// Not /meta.json: Inspect must derive the meta URL from the host.
			got, err := New(server.Client(), "").Inspect(context.Background(), server.URL+"/products/some-coat")
			if err != nil {
				t.Fatalf("Inspect() error = %v, want nil", err)
			}
			if got.Platform != testCase.wantPlatform {
				t.Errorf("Platform = %q, want %q (reason: %s)", got.Platform, testCase.wantPlatform, got.Reason)
			}
			if got.Currency != testCase.wantCurrency {
				t.Errorf("Currency = %q, want %q", got.Currency, testCase.wantCurrency)
			}
			if got.ShopName != testCase.wantShopName {
				t.Errorf("ShopName = %q, want %q", got.ShopName, testCase.wantShopName)
			}
			if got.Reason == "" {
				t.Error("Reason is empty; every verdict should explain itself")
			}
		})
	}
}

// TestInspectBadInput covers genuine errors, as opposed to a negative result.
func TestInspectBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"no scheme", "everlane.com/products/foo"},
		{"unsupported scheme", "ftp://everlane.com/x"},
		{"not a url", "://///"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(nil, "").Inspect(context.Background(), testCase.url); err == nil {
				t.Errorf("Inspect(%q) error = nil, want an error", testCase.url)
			}
		})
	}
}
