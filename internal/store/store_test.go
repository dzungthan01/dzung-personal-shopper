package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
)

// openTestStore returns a Store backed by a throwaway file. Migrations run, so
// each test exercises the real schema.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestMigrationsApply(t *testing.T) {
	store := openTestStore(t)

	for _, table := range []string{"brands", "items", "observations", "goose_db_version"} {
		var name string
		err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing after migration: %v", table, err)
		}
	}
}

func TestBrandUpsert(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// FRAME is the case the brand+".com" heuristic gets wrong: frame.com 404s,
	// so the domain has to be corrected by hand.
	brand := &model.Brand{Name: "FRAME", Slug: "frame", Domain: "frame.com", Platform: "unknown"}
	if _, err := store.UpsertBrand(ctx, brand); err != nil {
		t.Fatalf("UpsertBrand() error = %v", err)
	}

	verified := time.Now().UTC().Truncate(time.Second)
	corrected := &model.Brand{
		Name: "FRAME", Slug: "frame", Domain: "frame-store.com",
		Platform: "shopify", Currency: "USD", VerifiedAt: &verified,
	}
	id, err := store.UpsertBrand(ctx, corrected)
	if err != nil {
		t.Fatalf("UpsertBrand() second call error = %v", err)
	}
	if id != brand.ID {
		t.Errorf("upsert created a new row (id %d, want %d); slug should be the conflict key", id, brand.ID)
	}

	got, err := store.BrandBySlug(ctx, "frame")
	if err != nil {
		t.Fatalf("BrandBySlug() error = %v", err)
	}
	if got.Domain != "frame-store.com" {
		t.Errorf("Domain = %q, want the corrected frame-store.com", got.Domain)
	}
	if got.Platform != "shopify" {
		t.Errorf("Platform = %q, want shopify", got.Platform)
	}
	if got.VerifiedAt == nil || !got.VerifiedAt.Equal(verified) {
		t.Errorf("VerifiedAt = %v, want %v", got.VerifiedAt, verified)
	}
}

func TestBrandNotFound(t *testing.T) {
	store := openTestStore(t)
	_, err := store.BrandBySlug(context.Background(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestItemLifecycle(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	brand := &model.Brand{Name: "Veronica Beard", Slug: "veronicabeard", Domain: "veronicabeard.com", Platform: "shopify"}
	brandID, err := store.UpsertBrand(ctx, brand)
	if err != nil {
		t.Fatalf("UpsertBrand() error = %v", err)
	}

	item := &model.Item{
		URL:     "https://www.veronicabeard.com/products/belvedere-knit-tank-top-ecru",
		BrandID: &brandID,
		Source:  "shopify",
		Title:   "Belvedere Knit Tank Top",
		Variant: "M",
	}
	if _, err := store.AddItem(ctx, item); err != nil {
		t.Fatalf("AddItem() error = %v", err)
	}

	got, err := store.ItemByURLAndVariant(ctx, item.URL, "M")
	if err != nil {
		t.Fatalf("ItemByURLAndVariant() error = %v", err)
	}
	if got.Title != item.Title || got.Variant != "M" {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
	if got.BrandID == nil || *got.BrandID != brandID {
		t.Errorf("BrandID = %v, want %d", got.BrandID, brandID)
	}
	if got.Archived() {
		t.Error("new item reports as archived")
	}

	active, err := store.ListItems(ctx, false)
	if err != nil {
		t.Fatalf("ListItems() error = %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("ListItems() returned %d items, want 1", len(active))
	}

	if err := store.ArchiveItem(ctx, item.ID); err != nil {
		t.Fatalf("ArchiveItem() error = %v", err)
	}

	active, _ = store.ListItems(ctx, false)
	if len(active) != 0 {
		t.Errorf("archived item still listed as active: %d rows", len(active))
	}
	all, _ := store.ListItems(ctx, true)
	if len(all) != 1 {
		t.Errorf("archived item vanished from includeArchived listing: %d rows", len(all))
	}
}

func TestObservationsAreAppendOnly(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	item := &model.Item{URL: "https://frame-store.com/products/le-high-straight", Source: "shopify", Title: "Le High Straight"}
	if _, err := store.AddItem(ctx, item); err != nil {
		t.Fatalf("AddItem() error = %v", err)
	}

	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
	compareAt := int64(24800)

	readings := []model.Observation{
		{ItemID: item.ID, FetchedAt: base, PriceCents: 24800, Currency: "USD", Available: true,
			Variants: []model.Variant{{Size: "26", Available: true, PriceCents: 24800}}},
		{ItemID: item.ID, FetchedAt: base.Add(time.Hour), PriceCents: 17360, CompareCents: &compareAt,
			Currency: "USD", Available: true,
			Variants: []model.Variant{{Size: "26", SKU: "LHS-26", Available: false, PriceCents: 17360}, {Size: "27", Available: true, PriceCents: 17360}}},
	}
	for i := range readings {
		if _, err := store.AddObservation(ctx, &readings[i]); err != nil {
			t.Fatalf("AddObservation(%d) error = %v", i, err)
		}
	}

	latest, err := store.LatestObservation(ctx, item.ID)
	if err != nil {
		t.Fatalf("LatestObservation() error = %v", err)
	}
	if latest.PriceCents != 17360 {
		t.Errorf("PriceCents = %d, want the newer 17360", latest.PriceCents)
	}
	if !latest.OnSale() {
		t.Error("OnSale() = false, want true when compare_cents exceeds price")
	}
	if len(latest.Variants) != 2 {
		t.Fatalf("Variants round-tripped as %d entries, want 2", len(latest.Variants))
	}
	if latest.Variants[0].SKU != "LHS-26" || latest.Variants[0].Available {
		t.Errorf("variant round-trip lost data: %+v", latest.Variants[0])
	}

	history, err := store.ObservationHistory(ctx, item.ID, 0)
	if err != nil {
		t.Fatalf("ObservationHistory() error = %v", err)
	}
	if len(history) != 2 {
		t.Errorf("history has %d rows, want 2; the first reading must survive the second", len(history))
	}
	if !history[0].FetchedAt.After(history[1].FetchedAt) {
		t.Error("history is not newest-first")
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	store := openTestStore(t)
	orphan := &model.Observation{ItemID: 999, PriceCents: 100, Currency: "USD"}
	if _, err := store.AddObservation(context.Background(), orphan); err == nil {
		t.Error("inserted an observation for a nonexistent item; PRAGMA foreign_keys is not on")
	}
}

func TestDeletingItemCascadesObservations(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	item := &model.Item{URL: "https://frame-store.com/products/gone", Source: "shopify", Title: "Gone"}
	if _, err := store.AddItem(ctx, item); err != nil {
		t.Fatalf("AddItem() error = %v", err)
	}
	observation := &model.Observation{ItemID: item.ID, PriceCents: 100, Currency: "USD", Available: true}
	if _, err := store.AddObservation(ctx, observation); err != nil {
		t.Fatalf("AddObservation() error = %v", err)
	}

	if _, err := store.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, item.ID); err != nil {
		t.Fatalf("delete item: %v", err)
	}

	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE item_id = ?`, item.ID).Scan(&remaining); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d observations survived their item; ON DELETE CASCADE is not working", remaining)
	}
}
