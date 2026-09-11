package rules

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
)

var now = time.Date(2026, 9, 10, 14, 30, 0, 0, time.UTC)

func reading(priceCents int64, available bool, variants ...model.Variant) *model.Observation {
	return &model.Observation{PriceCents: priceCents, Currency: "USD", Available: available, Variants: variants}
}

func onSale(observation *model.Observation, compareCents int64) *model.Observation {
	observation.CompareCents = &compareCents
	return observation
}

func inStock(size string) model.Variant { return model.Variant{Size: size, Available: true} }
func soldOut(size string) model.Variant { return model.Variant{Size: size, Available: false} }

func kinds(alerts []model.Alert) []model.AlertKind {
	result := make([]model.AlertKind, 0, len(alerts))
	for _, alert := range alerts {
		result = append(result, alert.Kind)
	}
	return result
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name     string
		variant  string
		previous *model.Observation
		current  *model.Observation
		want     []model.AlertKind
	}{
		{
			name:     "first reading is a baseline",
			previous: nil,
			current:  reading(24800, true),
			want:     []model.AlertKind{},
		},
		{
			name:     "price drop",
			previous: reading(24800, true),
			current:  reading(21000, true),
			want:     []model.AlertKind{model.AlertPriceDrop},
		},
		{
			name:     "price rise is not news",
			previous: reading(21000, true),
			current:  reading(24800, true),
			want:     []model.AlertKind{},
		},
		{
			// 1% of $248 is $2.48, which beats the $1 floor.
			name:     "drop under 1% is noise",
			previous: reading(24800, true),
			current:  reading(24600, true),
			want:     []model.AlertKind{},
		},
		{
			name:     "drop exactly at the threshold fires",
			previous: reading(24800, true),
			current:  reading(24552, true),
			want:     []model.AlertKind{model.AlertPriceDrop},
		},
		{
			// 1% of $20 is 20 cents, so the $1 floor applies.
			name:     "cheap item uses the one-dollar floor",
			previous: reading(2000, true),
			current:  reading(1950, true),
			want:     []model.AlertKind{},
		},
		{
			name:     "sale start reports once, not also as a drop",
			previous: reading(24800, true),
			current:  onSale(reading(17300, true), 24800),
			want:     []model.AlertKind{model.AlertSaleStarted},
		},
		{
			name:     "deeper drop during an existing sale",
			previous: onSale(reading(17300, true), 24800),
			current:  onSale(reading(15000, true), 24800),
			want:     []model.AlertKind{model.AlertPriceDrop},
		},
		{
			name:     "sale ending is not news",
			previous: onSale(reading(17300, true), 24800),
			current:  reading(24800, true),
			want:     []model.AlertKind{},
		},
		{
			name:     "back in stock, no size set",
			previous: reading(24800, false),
			current:  reading(24800, true),
			want:     []model.AlertKind{model.AlertBackInStock},
		},
		{
			name:     "selling out is not news",
			previous: reading(24800, true),
			current:  reading(24800, false),
			want:     []model.AlertKind{},
		},
		{
			name:     "my variant back",
			variant:  "M",
			previous: reading(24800, true, inStock("S"), soldOut("M")),
			current:  reading(24800, true, inStock("S"), inStock("M")),
			want:     []model.AlertKind{model.AlertVariantBack},
		},
		{
			name:     "another variant back is not news",
			variant:  "M",
			previous: reading(24800, true, soldOut("S"), soldOut("M")),
			current:  reading(24800, true, inStock("S"), soldOut("M")),
			want:     []model.AlertKind{},
		},
		{
			name:     "with a variant set, whole-item restock reports only the variant",
			variant:  "M",
			previous: reading(24800, false, soldOut("M")),
			current:  reading(24800, true, inStock("M")),
			want:     []model.AlertKind{model.AlertVariantBack},
		},
		{
			name:     "with a variant set, restock without my variant is not news",
			variant:  "M",
			previous: reading(24800, false, soldOut("S"), soldOut("M")),
			current:  reading(24800, true, inStock("S"), soldOut("M")),
			want:     []model.AlertKind{},
		},
		{
			// A manual snapshot may carry no variants; unknown is not the same as sold out.
			name:     "previous reading with no variant data cannot prove a restock",
			variant:  "M",
			previous: reading(24800, true),
			current:  reading(24800, true, inStock("M")),
			want:     []model.AlertKind{},
		},
		{
			name:     "one reading can raise several alerts",
			variant:  "M",
			previous: reading(24800, true, soldOut("M")),
			current:  onSale(reading(17300, true, inStock("M")), 24800),
			want:     []model.AlertKind{model.AlertSaleStarted, model.AlertVariantBack},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			item := model.Item{ID: 7, Title: "Classic Easy Tote", URL: "https://cuyana.com/products/classic-easy-tote", Variant: testCase.variant}
			got := Evaluate(item, testCase.previous, testCase.current, now)
			assert.Equal(t, testCase.want, kinds(got))
		})
	}
}

func TestEvaluateBuildsDedupeKeys(t *testing.T) {
	item := model.Item{ID: 7, Variant: "M"}
	previous := reading(24800, true, soldOut("M"))
	current := onSale(reading(17300, true, inStock("M")), 24800)

	alerts := Evaluate(item, previous, current, now)
	require.Len(t, alerts, 2)
	assert.Equal(t, "item:7|kind:sale_started|to:17300|day:2026-09-10", alerts[0].DedupeKey)
	assert.Equal(t, "item:7|kind:variant_back|variant:M|day:2026-09-10", alerts[1].DedupeKey)
}

func TestDedupeKeyUsesUTCDay(t *testing.T) {
	// 11pm in New York on the 9th is already the 10th in UTC.
	newYork := time.FixedZone("EDT", -4*60*60)
	late := time.Date(2026, 9, 9, 23, 0, 0, 0, newYork)

	assert.Equal(t, "item:1|kind:price_drop|to:100|day:2026-09-10",
		DedupeKey(1, model.AlertPriceDrop, "to:100", late))
}

func TestEvaluateFillsPayload(t *testing.T) {
	item := model.Item{ID: 7, Title: "Classic Easy Tote", URL: "https://cuyana.com/products/classic-easy-tote"}
	previous := reading(24800, true)
	current := onSale(reading(17300, true), 24800)

	alerts := Evaluate(item, previous, current, now)
	require.Len(t, alerts, 1)

	assert.Equal(t, model.AlertPayload{
		Title: "Classic Easy Tote", URL: "https://cuyana.com/products/classic-easy-tote",
		Currency: "USD", PriceCents: 17300, PreviousCents: 24800, CompareCents: 24800,
	}, alerts[0].Payload)
	assert.Equal(t, int64(7500), alerts[0].Payload.DropCents())
	assert.Equal(t, now, alerts[0].CreatedAt)
}

func TestEvaluateMatchesVariantByFullName(t *testing.T) {
	item := model.Item{ID: 7, Variant: "Light Pistachio / M"}
	named := func(name, size string, available bool) model.Variant {
		return model.Variant{Name: name, Size: size, Available: available}
	}
	previous := reading(24800, true, named("Light Pistachio / S", "S", true), named("Light Pistachio / M", "M", false))
	current := reading(24800, true, named("Light Pistachio / S", "S", true), named("Light Pistachio / M", "M", true))

	alerts := Evaluate(item, previous, current, now)
	require.Len(t, alerts, 1)
	assert.Equal(t, model.AlertVariantBack, alerts[0].Kind)
	assert.Equal(t, "Light Pistachio / M", alerts[0].Payload.Variant)
}
