-- +goose Up

-- brands maps a brand name to the storefront that sells it. Seeded by hand;
-- see PLAN.md 4.1 for the guess-then-verify automation.
CREATE TABLE brands (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,   -- display name, e.g. "FRAME"
    slug        TEXT NOT NULL UNIQUE,   -- lookup key, e.g. "frame"
    domain      TEXT NOT NULL UNIQUE,   -- e.g. "frame-store.com"
    platform    TEXT NOT NULL,          -- "shopify" or "unknown"
    currency    TEXT,                   -- ISO code, when the store advertises one
    verified_at TEXT,                   -- RFC3339 UTC; NULL until detection confirms it
    created_at  TEXT NOT NULL
);

-- items is one row per thing you want. Mutable.
CREATE TABLE items (
    id          INTEGER PRIMARY KEY,
    url         TEXT NOT NULL UNIQUE,
    brand_id    INTEGER REFERENCES brands(id) ON DELETE SET NULL,
    source      TEXT NOT NULL,          -- which Source fetches it: "shopify" or "manual"
    title       TEXT NOT NULL,
    my_size     TEXT,                   -- verbatim; no cross-region normalization in v1
    image_url   TEXT,
    notes       TEXT,
    added_at    TEXT NOT NULL,
    archived_at TEXT                    -- soft delete, so price history survives
);

CREATE INDEX idx_items_brand ON items(brand_id);

-- observations is one row per price check. APPEND-ONLY: never updated, never
-- overwritten. A price drop is a comparison of the two newest rows.
CREATE TABLE observations (
    id            INTEGER PRIMARY KEY,
    item_id       INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    fetched_at    TEXT NOT NULL,
    price_cents   INTEGER NOT NULL,     -- minor units; never a float
    compare_cents INTEGER,              -- the "was" price; NULL when not on sale
    currency      TEXT NOT NULL,
    available     INTEGER NOT NULL,     -- 0 or 1; SQLite has no boolean type
    variants_json TEXT                  -- [{"size","sku","available","price_cents"}]
);

-- Serves the hot query: the newest observations for one item.
CREATE INDEX idx_observations_item_time ON observations(item_id, fetched_at DESC);

-- +goose Down

DROP INDEX idx_observations_item_time;
DROP TABLE observations;
DROP INDEX idx_items_brand;
DROP TABLE items;
DROP TABLE brands;
