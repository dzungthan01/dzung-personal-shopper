// Package rules decides which changes between two readings of an item are worth
// telling the user about. It does no I/O, so every rule is testable in isolation.
package rules

import (
	"fmt"
	"time"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
)

// minimumDropCents is the smallest drop worth an alert. Below 1% or this much,
// whichever is larger, a change is usually rounding or a tax-display quirk.
const minimumDropCents = 100

// Evaluate compares two readings of an item and returns the alerts they warrant.
// A nil previous is the item's first reading: a baseline, not a change.
// Currency is assumed not to change between readings.
func Evaluate(item model.Item, previous, current *model.Observation, now time.Time) []model.Alert {
	if previous == nil || current == nil {
		return nil
	}
	change := change{item: item, previous: previous, current: current, now: now}

	var alerts []model.Alert

	// A sale starting is usually also a price drop; reporting both would ping twice.
	switch {
	case !previous.OnSale() && current.OnSale():
		alerts = append(alerts, change.alert(model.AlertSaleStarted, priceValue(current.PriceCents)))
	case dropped(previous.PriceCents, current.PriceCents):
		alerts = append(alerts, change.alert(model.AlertPriceDrop, priceValue(current.PriceCents)))
	}

	// With a variant set, only that variant matters; otherwise the item as a whole.
	if item.Variant != "" {
		// No variants on the previous reading means its stock was unknown, not out.
		if len(previous.Variants) > 0 &&
			!model.VariantInStock(previous.Variants, item.Variant) &&
			model.VariantInStock(current.Variants, item.Variant) {
			alerts = append(alerts, change.alert(model.AlertVariantBack, "variant:"+item.Variant))
		}
	} else if !previous.Available && current.Available {
		alerts = append(alerts, change.alert(model.AlertBackInStock, "available"))
	}

	return alerts
}

// dropped reports whether the price fell by at least 1% or minimumDropCents.
func dropped(previousCents, currentCents int64) bool {
	threshold := max(minimumDropCents, previousCents/100)
	return previousCents-currentCents >= threshold
}

func priceValue(cents int64) string { return fmt.Sprintf("to:%d", cents) }

// DedupeKey caps repeats at one alert per item, kind, value and UTC day.
func DedupeKey(itemID int64, kind model.AlertKind, value string, now time.Time) string {
	return fmt.Sprintf("item:%d|kind:%s|%s|day:%s", itemID, kind, value, now.UTC().Format(time.DateOnly))
}

type change struct {
	item              model.Item
	previous, current *model.Observation
	now               time.Time
}

func (c change) alert(kind model.AlertKind, value string) model.Alert {
	alert := model.Alert{
		ItemID:    c.item.ID,
		Kind:      kind,
		DedupeKey: DedupeKey(c.item.ID, kind, value, c.now),
		CreatedAt: c.now,
		Payload: model.AlertPayload{
			Title:         c.item.Title,
			URL:           c.item.URL,
			Currency:      c.current.Currency,
			Variant:       c.item.Variant,
			PriceCents:    c.current.PriceCents,
			PreviousCents: c.previous.PriceCents,
		},
	}
	if c.current.CompareCents != nil {
		alert.Payload.CompareCents = *c.current.CompareCents
	}
	return alert
}
