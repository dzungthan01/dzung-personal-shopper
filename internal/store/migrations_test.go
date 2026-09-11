package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// databaseAtVersion returns a database migrated only as far as version, so a
// later migration can be exercised against realistic existing data.
func databaseAtVersion(t *testing.T, version int64) *sql.DB {
	t.Helper()
	db, err := connect(filepath.Join(t.TempDir(), "migrate.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, configureMigrations())
	require.NoError(t, goose.UpTo(db, "migrations", version))
	return db
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n))
	return n
}

// The table rebuild in 00003 drops items. With foreign keys on, that cascades
// and silently deletes every observation and alert.
func TestMigration3PreservesHistory(t *testing.T) {
	db := databaseAtVersion(t, 2)
	_, err := db.Exec(`INSERT INTO items (url, source, title, my_size, added_at)
		VALUES ('https://cuyana.com/products/classic-easy-tote', 'shopify', 'Classic Easy Tote', 'M', '2026-09-10T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO observations (item_id, fetched_at, price_cents, currency, available)
		VALUES (1, '2026-09-10T00:00:00Z', 24800, 'USD', 1)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO alerts (item_id, kind, dedupe_key, created_at)
		VALUES (1, 'price_drop', 'k', '2026-09-10T00:00:00Z')`)
	require.NoError(t, err)

	require.NoError(t, goose.UpTo(db, "migrations", 3))

	assert.Equal(t, 1, count(t, db, "observations"), "price history must survive the rebuild")
	assert.Equal(t, 1, count(t, db, "alerts"), "alerts must survive the rebuild")

	var variant string
	require.NoError(t, db.QueryRow(`SELECT variant FROM items WHERE id = 1`).Scan(&variant))
	assert.Equal(t, "M", variant, "my_size carries over as variant")

	rows, err := db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	assert.False(t, rows.Next(), "rebuild left dangling foreign keys")
	require.NoError(t, rows.Close())

	// Foreign keys must be back on afterwards.
	_, err = db.Exec(`INSERT INTO observations (item_id, fetched_at, price_cents, currency, available)
		VALUES (999, 't', 1, 'USD', 1)`)
	assert.Error(t, err, "foreign keys were left off")
}

func TestMigration3AllowsOneEntryPerVariant(t *testing.T) {
	db := databaseAtVersion(t, 3)
	insert := `INSERT INTO items (url, source, title, variant, added_at) VALUES (?, 'shopify', 'Tote', ?, 't')`
	url := "https://cuyana.com/products/classic-easy-tote"

	_, err := db.Exec(insert, url, "Black")
	require.NoError(t, err)
	_, err = db.Exec(insert, url, "Tan")
	require.NoError(t, err, "a second variant of the same product must be allowed")
	_, err = db.Exec(insert, url, "Black")
	assert.Error(t, err, "the same variant twice must be rejected")

	_, err = db.Exec(insert, url, "")
	require.NoError(t, err)
	_, err = db.Exec(insert, url, "")
	assert.Error(t, err, `"any variant" twice must be rejected; NOT NULL keeps it in the constraint`)
}

func TestMigration3DownKeepsFirstVariant(t *testing.T) {
	db := databaseAtVersion(t, 3)
	url := "https://cuyana.com/products/classic-easy-tote"
	for _, variant := range []string{"Black", "Tan"} {
		_, err := db.Exec(`INSERT INTO items (url, source, title, variant, added_at) VALUES (?, 'shopify', 'Tote', ?, 't')`, url, variant)
		require.NoError(t, err)
	}
	for itemID := 1; itemID <= 2; itemID++ {
		_, err := db.Exec(`INSERT INTO observations (item_id, fetched_at, price_cents, currency, available)
			VALUES (?, 't', 24800, 'USD', 1)`, itemID)
		require.NoError(t, err)
	}

	require.NoError(t, goose.Down(db, "migrations"))

	assert.Equal(t, 1, count(t, db, "items"), "the old schema holds one entry per URL")
	assert.Equal(t, 1, count(t, db, "observations"), "the dropped entry's history goes with it, not left orphaned")

	var mySize string
	require.NoError(t, db.QueryRow(`SELECT my_size FROM items`).Scan(&mySize))
	assert.Equal(t, "Black", mySize, "the first-added variant survives")
}
