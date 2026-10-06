package store

import (
	"context"
	"fmt"
)

// Counts is the size of everything the wishlist has recorded.
type Counts struct {
	ItemsActive        int
	ItemsArchived      int
	Observations       int
	AlertsPending      int // not yet acknowledged
	AlertsAcknowledged int
}

// Count totals items, observations and alerts in one query.
func (s *Store) Count(ctx context.Context) (Counts, error) {
	var counts Counts
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM items WHERE archived_at IS NULL),
		(SELECT COUNT(*) FROM items WHERE archived_at IS NOT NULL),
		(SELECT COUNT(*) FROM observations),
		(SELECT COUNT(*) FROM alerts WHERE read_at IS NULL),
		(SELECT COUNT(*) FROM alerts WHERE read_at IS NOT NULL)`,
	).Scan(&counts.ItemsActive, &counts.ItemsArchived, &counts.Observations,
		&counts.AlertsPending, &counts.AlertsAcknowledged)
	if err != nil {
		return Counts{}, fmt.Errorf("count rows: %w", err)
	}
	return counts, nil
}
