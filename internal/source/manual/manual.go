// Package manual is the Source for stores that cannot be fetched automatically.
//
// It never performs I/O. Prices for these items arrive through the
// record_snapshot tool, from a page the user or Claude has already loaded. That
// keeps the tool out of any fight with bot protection.
package manual

import (
	"context"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
	"github.com/dzungthan01/dzung-personal-shopper/internal/source"
)

// Name identifies this source in the items table.
const Name = "manual"

// Source implements source.Source for hand-entered prices.
type Source struct{}

// New returns the manual source.
func New() *Source { return &Source{} }

// Name returns "manual".
func (s *Source) Name() string { return Name }

// Fetch always fails: manual items have no endpoint to poll.
func (s *Source) Fetch(context.Context, string) (*model.Snapshot, error) {
	return nil, source.ErrManualOnly
}
