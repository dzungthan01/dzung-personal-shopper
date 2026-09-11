-- +goose NO TRANSACTION

-- +goose Up

-- One entry per URL and variant, so a product can be tracked in several
-- sizes or colours. SQLite cannot drop a constraint, so the table is rebuilt.
-- Foreign keys must be off first or DROP TABLE cascades and deletes all price
-- history. That PRAGMA is ignored inside a transaction, hence NO TRANSACTION.
PRAGMA foreign_keys = OFF;
BEGIN;

CREATE TABLE items_new (
    id          INTEGER PRIMARY KEY,
    url         TEXT NOT NULL,
    brand_id    INTEGER REFERENCES brands(id) ON DELETE SET NULL,
    source      TEXT NOT NULL,
    title       TEXT NOT NULL,
    variant     TEXT NOT NULL DEFAULT '',  -- '' means any. NOT NULL, since UNIQUE treats NULLs as distinct
    image_url   TEXT,
    notes       TEXT,
    added_at    TEXT NOT NULL,
    archived_at TEXT,
    UNIQUE (url, variant)
);

INSERT INTO items_new (id, url, brand_id, source, title, variant, image_url, notes, added_at, archived_at)
SELECT id, url, brand_id, source, title, COALESCE(my_size, ''), image_url, notes, added_at, archived_at
FROM items;

DROP TABLE items;
ALTER TABLE items_new RENAME TO items;
CREATE INDEX idx_items_brand ON items(brand_id);

COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down

-- Lossy: the old schema allows one entry per URL, so only the first-added
-- variant of each product survives, along with its history.
PRAGMA foreign_keys = OFF;
BEGIN;

DELETE FROM observations WHERE item_id NOT IN (SELECT MIN(id) FROM items GROUP BY url);
DELETE FROM alerts WHERE item_id NOT IN (SELECT MIN(id) FROM items GROUP BY url);

CREATE TABLE items_old (
    id          INTEGER PRIMARY KEY,
    url         TEXT NOT NULL UNIQUE,
    brand_id    INTEGER REFERENCES brands(id) ON DELETE SET NULL,
    source      TEXT NOT NULL,
    title       TEXT NOT NULL,
    my_size     TEXT,
    image_url   TEXT,
    notes       TEXT,
    added_at    TEXT NOT NULL,
    archived_at TEXT
);

INSERT INTO items_old (id, url, brand_id, source, title, my_size, image_url, notes, added_at, archived_at)
SELECT id, url, brand_id, source, title, variant, image_url, notes, added_at, archived_at
FROM items
WHERE id IN (SELECT MIN(id) FROM items GROUP BY url);

DROP TABLE items;
ALTER TABLE items_old RENAME TO items;
CREATE INDEX idx_items_brand ON items(brand_id);

COMMIT;
PRAGMA foreign_keys = ON;
