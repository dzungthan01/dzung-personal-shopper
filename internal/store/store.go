// Package store persists the wishlist in SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go driver, registers as "sqlite"

	"github.com/dzungthan01/dzung-personal-shopper/internal/model"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// timeLayout is how every timestamp is stored: RFC3339 in UTC, which sorts
// correctly as text.
const timeLayout = time.RFC3339Nano

// Store owns the database connection. Use Open to construct one.
type Store struct {
	db *sql.DB
}

// DefaultPath returns the database location, honoring XDG_DATA_HOME.
// Both "start" and "watch" must resolve to the same file.
func DefaultPath() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home directory: %w", err)
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "dzung-personal-shopper", "shopper.db"), nil
}

// Open connects to the database at path, creating it if needed, and applies
// any pending migrations. Pass ":memory:" for tests.
func Open(dbPath string) (*Store, error) {
	db, err := connect(dbPath)
	if err != nil {
		return nil, err
	}
	if err := configureMigrations(); err != nil {
		db.Close()
		return nil, err
	}
	if err := goose.Up(db, "migrations"); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return &Store{db: db}, nil
}

// connect opens the database file, creating its directory if needed.
func connect(dbPath string) (*sql.DB, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
	}

	// WAL lets "start" and "watch" hold the file open at once; foreign_keys is
	// off by default in SQLite and must be requested per connection.
	dataSourceName := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", dbPath)

	db, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// One connection, so a PRAGMA always applies to the connection that runs the
	// next statement; migration 00003 depends on it. SQLite has one writer anyway.
	// Never hold rows open while issuing another query: it would wait forever.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return db, nil
}

func configureMigrations() error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(log.New(os.Stderr, "goose: ", 0)) // never stdout: that is MCP's JSON-RPC stream
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	return nil
}

// Close releases the database connection.
func (s *Store) Close() error { return s.db.Close() }

// UpsertBrand inserts a brand or updates the existing row with the same slug.
func (s *Store) UpsertBrand(ctx context.Context, brand *model.Brand) (int64, error) {
	if brand.CreatedAt.IsZero() {
		brand.CreatedAt = time.Now().UTC()
	}
	const query = `
		INSERT INTO brands (name, slug, domain, platform, currency, verified_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
			name = excluded.name,
			domain = excluded.domain,
			platform = excluded.platform,
			currency = excluded.currency,
			verified_at = excluded.verified_at
		RETURNING id`

	var id int64
	err := s.db.QueryRowContext(ctx, query,
		brand.Name, brand.Slug, brand.Domain, brand.Platform, brand.Currency,
		formatNullTime(brand.VerifiedAt), formatTime(brand.CreatedAt),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert brand %q: %w", brand.Slug, err)
	}
	brand.ID = id
	return id, nil
}

const brandColumns = `id, name, slug, domain, platform, currency, verified_at, created_at`

// BrandBySlug looks up a brand by its slug, returning ErrNotFound if absent.
func (s *Store) BrandBySlug(ctx context.Context, slug string) (*model.Brand, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+brandColumns+` FROM brands WHERE slug = ?`, slug)
	brand, err := scanBrand(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("brand %q: %w", slug, ErrNotFound)
	}
	return brand, err
}

// ListBrands returns every known brand, alphabetically.
func (s *Store) ListBrands(ctx context.Context) ([]model.Brand, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+brandColumns+` FROM brands ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list brands: %w", err)
	}
	defer rows.Close()

	var brands []model.Brand
	for rows.Next() {
		brand, err := scanBrand(rows)
		if err != nil {
			return nil, err
		}
		brands = append(brands, *brand)
	}
	return brands, rows.Err()
}

// AddItem inserts a wishlist item and returns its id.
func (s *Store) AddItem(ctx context.Context, item *model.Item) (int64, error) {
	if item.AddedAt.IsZero() {
		item.AddedAt = time.Now().UTC()
	}
	const query = `
		INSERT INTO items (url, brand_id, source, title, variant, image_url, notes, added_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`

	var id int64
	err := s.db.QueryRowContext(ctx, query,
		item.URL, item.BrandID, item.Source, item.Title,
		item.Variant, item.ImageURL, item.Notes, formatTime(item.AddedAt),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add item %q: %w", item.URL, err)
	}
	item.ID = id
	return id, nil
}

const itemColumns = `id, url, brand_id, source, title, variant, image_url, notes, added_at, archived_at`

// ItemByID looks up one item, returning ErrNotFound if absent.
func (s *Store) ItemByID(ctx context.Context, id int64) (*model.Item, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM items WHERE id = ?`, id)
	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("item %d: %w", id, ErrNotFound)
	}
	return item, err
}

// ItemByURLAndVariant looks up the entry for one URL and variant, returning
// ErrNotFound if absent. An empty variant is the entry tracking any variant.
func (s *Store) ItemByURLAndVariant(ctx context.Context, url, variant string) (*model.Item, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+itemColumns+` FROM items WHERE url = ? AND variant = ?`, url, variant)
	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("item %q variant %q: %w", url, variant, ErrNotFound)
	}
	return item, err
}

// ListItems returns wishlist items, newest first. Archived items are excluded
// unless includeArchived is set.
func (s *Store) ListItems(ctx context.Context, includeArchived bool) ([]model.Item, error) {
	query := `SELECT ` + itemColumns + ` FROM items`
	if !includeArchived {
		query += ` WHERE archived_at IS NULL`
	}
	query += ` ORDER BY added_at DESC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	defer rows.Close()

	var items []model.Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

// ArchiveItem soft-deletes an item, preserving its price history.
func (s *Store) ArchiveItem(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE items SET archived_at = ? WHERE id = ? AND archived_at IS NULL`,
		formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("archive item %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("item %d: %w", id, ErrNotFound)
	}
	return nil
}

// AddObservation appends a price reading. Observations are never updated.
func (s *Store) AddObservation(ctx context.Context, observation *model.Observation) (int64, error) {
	if observation.FetchedAt.IsZero() {
		observation.FetchedAt = time.Now().UTC()
	}
	variantsJSON, err := json.Marshal(observation.Variants)
	if err != nil {
		return 0, fmt.Errorf("encode variants: %w", err)
	}

	const query = `
		INSERT INTO observations (item_id, fetched_at, price_cents, compare_cents, currency, available, variants_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id`

	var id int64
	err = s.db.QueryRowContext(ctx, query,
		observation.ItemID, formatTime(observation.FetchedAt),
		observation.PriceCents, observation.CompareCents,
		observation.Currency, observation.Available, string(variantsJSON),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add observation for item %d: %w", observation.ItemID, err)
	}
	observation.ID = id
	return id, nil
}

const observationColumns = `id, item_id, fetched_at, price_cents, compare_cents, currency, available, variants_json`

// LatestObservation returns the most recent reading for an item.
func (s *Store) LatestObservation(ctx context.Context, itemID int64) (*model.Observation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+observationColumns+` FROM observations WHERE item_id = ? ORDER BY fetched_at DESC LIMIT 1`,
		itemID)
	observation, err := scanObservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("observations for item %d: %w", itemID, ErrNotFound)
	}
	return observation, err
}

// ObservationHistory returns an item's readings, newest first. A limit of 0
// means no limit.
func (s *Store) ObservationHistory(ctx context.Context, itemID int64, limit int) ([]model.Observation, error) {
	query := `SELECT ` + observationColumns + ` FROM observations WHERE item_id = ? ORDER BY fetched_at DESC`
	args := []any{itemID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("observation history for item %d: %w", itemID, err)
	}
	defer rows.Close()

	var observations []model.Observation
	for rows.Next() {
		observation, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		observations = append(observations, *observation)
	}
	return observations, rows.Err()
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanBrand(source scanner) (*model.Brand, error) {
	var brand model.Brand
	var currency, verifiedAt sql.NullString
	var createdAt string

	if err := source.Scan(&brand.ID, &brand.Name, &brand.Slug, &brand.Domain,
		&brand.Platform, &currency, &verifiedAt, &createdAt); err != nil {
		return nil, err
	}

	brand.Currency = currency.String
	parsed, err := parseNullTime(verifiedAt)
	if err != nil {
		return nil, fmt.Errorf("brand %d verified_at: %w", brand.ID, err)
	}
	brand.VerifiedAt = parsed
	if brand.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("brand %d created_at: %w", brand.ID, err)
	}
	return &brand, nil
}

func scanItem(source scanner) (*model.Item, error) {
	var item model.Item
	var brandID sql.NullInt64
	var imageURL, notes, archivedAt sql.NullString
	var addedAt string

	if err := source.Scan(&item.ID, &item.URL, &brandID, &item.Source, &item.Title,
		&item.Variant, &imageURL, &notes, &addedAt, &archivedAt); err != nil {
		return nil, err
	}

	if brandID.Valid {
		item.BrandID = &brandID.Int64
	}
	item.ImageURL = imageURL.String
	item.Notes = notes.String

	var err error
	if item.AddedAt, err = parseTime(addedAt); err != nil {
		return nil, fmt.Errorf("item %d added_at: %w", item.ID, err)
	}
	if item.ArchivedAt, err = parseNullTime(archivedAt); err != nil {
		return nil, fmt.Errorf("item %d archived_at: %w", item.ID, err)
	}
	return &item, nil
}

func scanObservation(source scanner) (*model.Observation, error) {
	var observation model.Observation
	var compareCents sql.NullInt64
	var variantsJSON sql.NullString
	var fetchedAt string

	if err := source.Scan(&observation.ID, &observation.ItemID, &fetchedAt,
		&observation.PriceCents, &compareCents, &observation.Currency,
		&observation.Available, &variantsJSON); err != nil {
		return nil, err
	}

	if compareCents.Valid {
		observation.CompareCents = &compareCents.Int64
	}
	if variantsJSON.Valid && variantsJSON.String != "" {
		if err := json.Unmarshal([]byte(variantsJSON.String), &observation.Variants); err != nil {
			return nil, fmt.Errorf("observation %d variants: %w", observation.ID, err)
		}
	}

	var err error
	if observation.FetchedAt, err = parseTime(fetchedAt); err != nil {
		return nil, fmt.Errorf("observation %d fetched_at: %w", observation.ID, err)
	}
	return &observation, nil
}

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func formatNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(timeLayout, value)
}

func parseNullTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
