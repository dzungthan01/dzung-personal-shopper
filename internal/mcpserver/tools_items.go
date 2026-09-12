package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dzungthan01/dzung-personal-shopper/internal/detect"
	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/manual"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source/shopify"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

type addItemInput struct {
	URL     string `json:"url" jsonschema:"the product page URL to track"`
	Variant string `json:"variant,omitempty" jsonschema:"the colour and/or size wanted, as the store names it, e.g. Light Pistachio / M, or just M. Leave empty to track any variant"`
	Title   string `json:"title,omitempty" jsonschema:"a title for the item; only used when the store cannot be read automatically"`
	Notes   string `json:"notes,omitempty" jsonschema:"free-text note to yourself about this item"`
}

type addItemOutput struct {
	ItemID            int64    `json:"item_id" jsonschema:"database id, used by the other tools"`
	Title             string   `json:"title"`
	Brand             string   `json:"brand,omitempty"`
	Source            string   `json:"source" jsonschema:"shopify if prices update automatically, manual if they must be recorded by hand"`
	AutoTracked       bool     `json:"auto_tracked" jsonschema:"true when prices can be refreshed without help"`
	PriceCents        int64    `json:"price_cents,omitempty" jsonschema:"current price in minor units, e.g. 17300 means $173.00"`
	CompareCents      int64    `json:"compare_cents,omitempty" jsonschema:"the pre-sale price, when the item is discounted"`
	Currency          string   `json:"currency,omitempty"`
	Available         bool     `json:"available,omitempty"`
	Variant           string   `json:"variant,omitempty" jsonschema:"the variant being tracked, as the store names it"`
	VariantInStock    *bool    `json:"variant_in_stock,omitempty" jsonschema:"whether the tracked variant is available; null when tracking any variant"`
	AvailableVariants []string `json:"available_variants,omitempty"`
	AlreadyTracked    bool     `json:"already_tracked,omitempty" jsonschema:"true if this URL and variant were already on the wishlist"`
	Message           string   `json:"message" jsonschema:"one line summarising what happened, safe to relay to the user"`
}

func registerAddItem(server *mcp.Server, dependencies Dependencies) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "add_item",
		Description: "Add a product to the wishlist. Detects whether the store can be read " +
			"automatically; if so, records the current price immediately. If the store cannot be " +
			"read (most luxury retailers block automated requests), the item is still tracked but " +
			"prices must be supplied with record_snapshot. To track one product in several sizes or " +
			"colours, add it once per variant.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input addItemInput) (*mcp.CallToolResult, addItemOutput, error) {
		if strings.TrimSpace(input.URL) == "" {
			return nil, addItemOutput{}, errors.New("url is required")
		}

		productURL := strings.TrimSpace(input.URL)

		detected, err := dependencies.Detector.Inspect(ctx, productURL)
		if err != nil {
			return nil, addItemOutput{}, fmt.Errorf("inspect %q: %w", productURL, err)
		}

		sourceName := manual.Name
		if detected.Platform == detect.PlatformShopify {
			sourceName = shopify.Name
		}
		productSource, err := dependencies.Sources.Get(sourceName)
		if err != nil {
			return nil, addItemOutput{}, err
		}

		// A manual source returns ErrManualOnly rather than a snapshot; that is
		// an expected outcome, not a failure.
		snapshot, err := productSource.Fetch(ctx, productURL)
		if err != nil && !errors.Is(err, source.ErrManualOnly) {
			return nil, addItemOutput{}, fmt.Errorf("fetch %q: %w", productURL, err)
		}

		// Checking needs the store's variant list, so it happens after the fetch.
		// An unknown variant is an error that names the real options, so the
		// caller can retry rather than track something that can never restock.
		variant := strings.TrimSpace(input.Variant)
		if snapshot != nil {
			if variant, err = snapshot.ResolveVariant(input.Variant); err != nil {
				return nil, addItemOutput{}, err
			}
		}

		if existing, err := dependencies.Store.ItemByURLAndVariant(ctx, productURL, variant); err == nil {
			return nil, addItemOutput{
				ItemID: existing.ID, Title: existing.Title, Source: existing.Source, Variant: existing.Variant,
				AutoTracked: existing.Source != manual.Name, AlreadyTracked: true,
				Message: fmt.Sprintf("Already tracking %s as item %d.", describe(existing.Title, existing.Variant), existing.ID),
			}, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, addItemOutput{}, err
		}

		item := &model.Item{
			URL:     productURL,
			Source:  sourceName,
			Title:   input.Title,
			Variant: variant,
			Notes:   input.Notes,
		}
		if snapshot != nil {
			item.Title = snapshot.Title
			item.ImageURL = snapshot.ImageURL
		}
		if item.Title == "" {
			item.Title = titleFromURL(productURL)
		}

		brandName := detected.ShopName
		if snapshot != nil && snapshot.Brand != "" {
			brandName = snapshot.Brand
		}
		if brandName != "" {
			brand := &model.Brand{
				Name: brandName, Slug: slugify(brandName), Domain: detected.Host,
				Platform: string(detected.Platform), Currency: detected.Currency,
			}
			if detected.Platform == detect.PlatformShopify {
				now := timeNow()
				brand.VerifiedAt = &now
			}
			if brandID, err := dependencies.Store.UpsertBrand(ctx, brand); err == nil {
				item.BrandID = &brandID
			}
		}

		if _, err := dependencies.Store.AddItem(ctx, item); err != nil {
			return nil, addItemOutput{}, err
		}

		output := addItemOutput{
			ItemID: item.ID, Title: item.Title, Brand: brandName,
			Source: sourceName, AutoTracked: sourceName != manual.Name, Variant: variant,
		}

		if snapshot == nil {
			output.Message = fmt.Sprintf(
				"Added %s as item %d. %s cannot be read automatically, so use record_snapshot to log its price.",
				describe(item.Title, item.Variant), item.ID, detected.Host)
			return nil, output, nil
		}

		if _, err := dependencies.Store.AddObservation(ctx, snapshot.Observation(item.ID)); err != nil {
			return nil, addItemOutput{}, err
		}

		output.PriceCents = snapshot.PriceCents
		output.CompareCents = snapshot.CompareCents
		output.Currency = snapshot.Currency
		output.Available = snapshot.Available
		output.AvailableVariants = snapshot.AvailableVariants()
		if variant != "" {
			inStock := model.VariantInStock(snapshot.Variants, variant)
			output.VariantInStock = &inStock
		}
		output.Message = fmt.Sprintf("Added %s as item %d at %s.",
			describe(item.Title, item.Variant), item.ID, model.FormatMoney(snapshot.PriceCents, snapshot.Currency))
		return nil, output, nil
	})
}

type listItemsInput struct {
	IncludeArchived bool `json:"include_archived,omitempty" jsonschema:"also list items removed from the wishlist"`
}

type wishlistEntry struct {
	ItemID         int64  `json:"item_id"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	Source         string `json:"source"`
	Variant        string `json:"variant,omitempty"`
	PriceCents     int64  `json:"price_cents,omitempty"`
	CompareCents   int64  `json:"compare_cents,omitempty"`
	Currency       string `json:"currency,omitempty"`
	OnSale         bool   `json:"on_sale,omitempty"`
	Available      bool   `json:"available,omitempty"`
	VariantInStock *bool  `json:"variant_in_stock,omitempty" jsonschema:"null when tracking any variant"`
	LastCheckedAt  string `json:"last_checked_at,omitempty" jsonschema:"RFC3339, empty when never checked"`
	Archived       bool   `json:"archived,omitempty"`
}

type listItemsOutput struct {
	Items []wishlistEntry `json:"items"`
	Count int             `json:"count"`
}

func registerListItems(server *mcp.Server, dependencies Dependencies) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_items",
		Description: "List everything on the wishlist with its most recent known price, whether " +
			"it is on sale, and whether the wanted size is in stock.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input listItemsInput) (*mcp.CallToolResult, listItemsOutput, error) {
		items, err := dependencies.Store.ListItems(ctx, input.IncludeArchived)
		if err != nil {
			return nil, listItemsOutput{}, err
		}

		output := listItemsOutput{Items: make([]wishlistEntry, 0, len(items))}
		for _, item := range items {
			entry := wishlistEntry{
				ItemID: item.ID, Title: item.Title, URL: item.URL,
				Source: item.Source, Variant: item.Variant, Archived: item.Archived(),
			}

			latest, err := dependencies.Store.LatestObservation(ctx, item.ID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, listItemsOutput{}, err
			}
			if latest != nil {
				entry.PriceCents = latest.PriceCents
				entry.Currency = latest.Currency
				entry.Available = latest.Available
				entry.OnSale = latest.OnSale()
				entry.LastCheckedAt = latest.FetchedAt.Format(rfc3339)
				if latest.CompareCents != nil {
					entry.CompareCents = *latest.CompareCents
				}
				if item.Variant != "" {
					inStock := model.VariantInStock(latest.Variants, item.Variant)
					entry.VariantInStock = &inStock
				}
			}
			output.Items = append(output.Items, entry)
		}
		output.Count = len(output.Items)
		return nil, output, nil
	})
}

type archiveItemInput struct {
	ItemID int64 `json:"item_id" jsonschema:"the id returned by add_item or list_items"`
}

type archiveItemOutput struct {
	ItemID  int64  `json:"item_id"`
	Message string `json:"message"`
}

func registerArchiveItem(server *mcp.Server, dependencies Dependencies) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "archive_item",
		Description: "Remove an item from the active wishlist. Its price history is kept, so it " +
			"can still be used to judge whether a future price is a good one.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input archiveItemInput) (*mcp.CallToolResult, archiveItemOutput, error) {
		if err := dependencies.Store.ArchiveItem(ctx, input.ItemID); err != nil {
			return nil, archiveItemOutput{}, err
		}
		return nil, archiveItemOutput{
			ItemID:  input.ItemID,
			Message: fmt.Sprintf("Archived item %d. Its price history is intact.", input.ItemID),
		}, nil
	})
}

// slugify reduces a brand name to a lookup key: "Veronica Beard" -> "veronicabeard".
// The same transform feeds the brand-to-domain guess described in PLAN.md 4.1.
func slugify(name string) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(name) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

// titleFromURL derives a readable title from a product slug, for stores we
// cannot read.
func titleFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	last := segments[len(segments)-1]
	if last == "" {
		return parsed.Host
	}
	return strings.TrimSpace(strings.ReplaceAll(last, "-", " "))
}

// describe names an item for messages: "Coleta Sweater" or "Coleta Sweater (M)".
func describe(title, variant string) string {
	if variant == "" {
		return fmt.Sprintf("%q", title)
	}
	return fmt.Sprintf("%q (%s)", title, variant)
}
