package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
	"github.com/dzungthan01/dzung-personal-shopper/internal/store"
)

const rfc3339 = time.RFC3339

type checkItemInput struct {
	ItemID int64 `json:"item_id" jsonschema:"the id returned by add_item or list_items"`
}

type checkItemOutput struct {
	ItemID            int64    `json:"item_id"`
	Title             string   `json:"title"`
	PriceCents        int64    `json:"price_cents"`
	PreviousCents     int64    `json:"previous_cents,omitempty" jsonschema:"price at the last check, absent on the first"`
	ChangeCents       int64    `json:"change_cents,omitempty" jsonschema:"negative means the price dropped"`
	Currency          string   `json:"currency"`
	OnSale            bool     `json:"on_sale"`
	Available         bool     `json:"available"`
	AvailableVariants []string `json:"available_variants,omitempty"`
	VariantInStock    *bool    `json:"variant_in_stock,omitempty" jsonschema:"null when tracking any variant"`
	BackInStock       bool     `json:"back_in_stock,omitempty" jsonschema:"true when it was unavailable at the last check"`
	Message           string   `json:"message"`
}

func registerCheckItem(server *mcp.Server, dependencies Dependencies) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "check_item",
		Description: "Fetch the current price and stock for one tracked item, record it, and " +
			"report what changed since the last check. Only works for automatically readable " +
			"stores; use record_snapshot for the others.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input checkItemInput) (*mcp.CallToolResult, checkItemOutput, error) {
		item, err := dependencies.Store.ItemByID(ctx, input.ItemID)
		if err != nil {
			return nil, checkItemOutput{}, err
		}

		productSource, err := dependencies.Sources.Get(item.Source)
		if err != nil {
			return nil, checkItemOutput{}, err
		}

		snapshot, err := productSource.Fetch(ctx, item.URL)
		if errors.Is(err, source.ErrManualOnly) {
			return nil, checkItemOutput{}, fmt.Errorf(
				"item %d is from a store that blocks automated reads; use record_snapshot instead", item.ID)
		}
		if err != nil {
			return nil, checkItemOutput{}, err
		}

		previous, err := dependencies.Store.LatestObservation(ctx, item.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, checkItemOutput{}, err
		}

		if _, err := dependencies.Store.AddObservation(ctx, observationFrom(item.ID, snapshot)); err != nil {
			return nil, checkItemOutput{}, err
		}

		output := checkItemOutput{
			ItemID: item.ID, Title: item.Title,
			PriceCents: snapshot.PriceCents, Currency: snapshot.Currency,
			OnSale:    snapshot.CompareCents > snapshot.PriceCents,
			Available: snapshot.Available, AvailableVariants: snapshot.AvailableVariants(),
		}
		if item.Variant != "" {
			inStock := model.VariantInStock(snapshot.Variants, item.Variant)
			output.VariantInStock = &inStock
		}

		if previous == nil {
			output.Message = fmt.Sprintf("First recorded price for %q: %s.",
				item.Title, formatMoney(snapshot.PriceCents, snapshot.Currency))
			return nil, output, nil
		}

		output.PreviousCents = previous.PriceCents
		output.ChangeCents = snapshot.PriceCents - previous.PriceCents
		output.BackInStock = snapshot.Available && !previous.Available

		switch {
		case output.ChangeCents < 0:
			output.Message = fmt.Sprintf("%q dropped %s to %s.", item.Title,
				formatMoney(-output.ChangeCents, snapshot.Currency),
				formatMoney(snapshot.PriceCents, snapshot.Currency))
		case output.ChangeCents > 0:
			output.Message = fmt.Sprintf("%q rose %s to %s.", item.Title,
				formatMoney(output.ChangeCents, snapshot.Currency),
				formatMoney(snapshot.PriceCents, snapshot.Currency))
		default:
			output.Message = fmt.Sprintf("%q is unchanged at %s.", item.Title,
				formatMoney(snapshot.PriceCents, snapshot.Currency))
		}
		if output.BackInStock {
			output.Message += " It is back in stock."
		}
		return nil, output, nil
	})
}

type recordSnapshotInput struct {
	ItemID            int64    `json:"item_id" jsonschema:"the id returned by add_item or list_items"`
	PriceCents        int64    `json:"price_cents" jsonschema:"price in minor units: $173.00 is 17300"`
	Currency          string   `json:"currency" jsonschema:"ISO code, e.g. USD"`
	CompareCents      int64    `json:"compare_cents,omitempty" jsonschema:"the crossed-out original price, when shown"`
	Available         bool     `json:"available" jsonschema:"whether the item is purchasable at all"`
	AvailableVariants []string `json:"available_variants,omitempty" jsonschema:"variants shown as in stock. For the item's own variant, use the name list_items shows for it. A missing region is fine (38 matches IT 38) but other spellings may not match"`
}

type recordSnapshotOutput struct {
	ItemID        int64  `json:"item_id"`
	PriceCents    int64  `json:"price_cents"`
	PreviousCents int64  `json:"previous_cents,omitempty"`
	ChangeCents   int64  `json:"change_cents,omitempty"`
	Message       string `json:"message"`
}

func registerRecordSnapshot(server *mcp.Server, dependencies Dependencies) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "record_snapshot",
		Description: "Record a price you read off a product page yourself. This is how stores that " +
			"block automated requests get tracked: read the page, then log what it says. Works for " +
			"any item, including automatically readable ones.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input recordSnapshotInput) (*mcp.CallToolResult, recordSnapshotOutput, error) {
		item, err := dependencies.Store.ItemByID(ctx, input.ItemID)
		if err != nil {
			return nil, recordSnapshotOutput{}, err
		}
		if input.PriceCents <= 0 {
			return nil, recordSnapshotOutput{}, errors.New("price_cents must be positive; $173.00 is 17300")
		}
		if strings.TrimSpace(input.Currency) == "" {
			return nil, recordSnapshotOutput{}, errors.New("currency is required, e.g. USD")
		}

		previous, err := dependencies.Store.LatestObservation(ctx, item.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, recordSnapshotOutput{}, err
		}

		observation := &model.Observation{
			ItemID: item.ID, PriceCents: input.PriceCents,
			Currency: strings.ToUpper(input.Currency), Available: input.Available,
		}
		if input.CompareCents > 0 {
			compare := input.CompareCents
			observation.CompareCents = &compare
		}
		for _, name := range input.AvailableVariants {
			observation.Variants = append(observation.Variants, model.Variant{
				Name: name, Available: true, PriceCents: input.PriceCents,
			})
		}

		if _, err := dependencies.Store.AddObservation(ctx, observation); err != nil {
			return nil, recordSnapshotOutput{}, err
		}

		output := recordSnapshotOutput{ItemID: item.ID, PriceCents: input.PriceCents}
		if previous != nil {
			output.PreviousCents = previous.PriceCents
			output.ChangeCents = input.PriceCents - previous.PriceCents
		}
		output.Message = fmt.Sprintf("Recorded %s for %q.",
			formatMoney(input.PriceCents, observation.Currency), item.Title)
		return nil, output, nil
	})
}

type priceHistoryInput struct {
	ItemID int64 `json:"item_id" jsonschema:"the id returned by add_item or list_items"`
	Limit  int   `json:"limit,omitempty" jsonschema:"how many readings to return, newest first; 0 means all"`
}

type priceReading struct {
	FetchedAt  string `json:"fetched_at" jsonschema:"RFC3339 timestamp"`
	PriceCents int64  `json:"price_cents"`
	Currency   string `json:"currency"`
	Available  bool   `json:"available"`
	OnSale     bool   `json:"on_sale,omitempty"`
}

type priceHistoryOutput struct {
	ItemID      int64          `json:"item_id"`
	Title       string         `json:"title"`
	Readings    []priceReading `json:"readings"`
	LowestSeen  int64          `json:"lowest_seen_cents,omitempty" jsonschema:"cheapest price ever recorded, for judging whether a price is good"`
	HighestSeen int64          `json:"highest_seen_cents,omitempty"`
	Currency    string         `json:"currency,omitempty"`
}

func registerPriceHistory(server *mcp.Server, dependencies Dependencies) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_price_history",
		Description: "Return every recorded price for an item, newest first, plus the lowest and " +
			"highest ever seen. Use it to judge whether a current price is actually a good one.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input priceHistoryInput) (*mcp.CallToolResult, priceHistoryOutput, error) {
		item, err := dependencies.Store.ItemByID(ctx, input.ItemID)
		if err != nil {
			return nil, priceHistoryOutput{}, err
		}
		observations, err := dependencies.Store.ObservationHistory(ctx, item.ID, input.Limit)
		if err != nil {
			return nil, priceHistoryOutput{}, err
		}

		output := priceHistoryOutput{
			ItemID: item.ID, Title: item.Title,
			Readings: make([]priceReading, 0, len(observations)),
		}
		for _, observation := range observations {
			if output.LowestSeen == 0 || observation.PriceCents < output.LowestSeen {
				output.LowestSeen = observation.PriceCents
			}
			if observation.PriceCents > output.HighestSeen {
				output.HighestSeen = observation.PriceCents
			}
			output.Currency = observation.Currency
			output.Readings = append(output.Readings, priceReading{
				FetchedAt:  observation.FetchedAt.Format(rfc3339),
				PriceCents: observation.PriceCents,
				Currency:   observation.Currency,
				Available:  observation.Available,
				OnSale:     observation.OnSale(),
			})
		}
		return nil, output, nil
	})
}

// observationFrom converts a Source snapshot into a storable observation.
func observationFrom(itemID int64, snapshot *model.Snapshot) *model.Observation {
	observation := &model.Observation{
		ItemID:     itemID,
		FetchedAt:  snapshot.FetchedAt,
		PriceCents: snapshot.PriceCents,
		Currency:   snapshot.Currency,
		Available:  snapshot.Available,
		Variants:   snapshot.Variants,
	}
	if snapshot.CompareCents > 0 {
		compare := snapshot.CompareCents
		observation.CompareCents = &compare
	}
	return observation
}

// formatMoney renders minor units for humans: 17300, "USD" -> "$173.00".
func formatMoney(cents int64, currency string) string {
	symbol := map[string]string{"USD": "$", "EUR": "€", "GBP": "£"}[strings.ToUpper(currency)]
	if symbol == "" {
		return fmt.Sprintf("%d.%02d %s", cents/100, cents%100, strings.ToUpper(currency))
	}
	return fmt.Sprintf("%s%d.%02d", symbol, cents/100, cents%100)
}

// timeNow is a variable so tests can freeze the clock.
var timeNow = func() time.Time { return time.Now().UTC() }
