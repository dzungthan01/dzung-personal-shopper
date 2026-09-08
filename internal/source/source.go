// Package source fetches live product data. Each retailer platform is one
// implementation; adding a retailer means adding one Source and nothing else.
package source

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
)

var (
	// ErrManualOnly means the store cannot be fetched automatically and the
	// price must be recorded by hand.
	ErrManualOnly = errors.New("store requires manual price entry")

	// ErrNotAProductURL means the URL does not point at a product page.
	ErrNotAProductURL = errors.New("url is not a product page")

	// ErrUnknownSource means no registered Source has that name.
	ErrUnknownSource = errors.New("unknown source")
)

// Source reads a product URL and returns a point-in-time snapshot.
type Source interface {
	Name() string
	Fetch(ctx context.Context, rawURL string) (*model.Snapshot, error)
}

// Registry looks up a Source by name. Which Source handles an item is decided
// once, when the item is added, and stored on the item; that avoids re-probing
// the store on every fetch.
type Registry struct {
	sources map[string]Source
}

// NewRegistry indexes the given sources by name.
func NewRegistry(sources ...Source) *Registry {
	indexed := make(map[string]Source, len(sources))
	for _, source := range sources {
		indexed[source.Name()] = source
	}
	return &Registry{sources: indexed}
}

// Get returns the named Source, or ErrUnknownSource.
func (r *Registry) Get(name string) (Source, error) {
	source, ok := r.sources[name]
	if !ok {
		return nil, fmt.Errorf("%q: %w", name, ErrUnknownSource)
	}
	return source, nil
}

// Names lists the registered sources, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.sources))
	for name := range r.sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
