-- +goose Up

-- alerts is one row per thing worth telling the user about. dedupe_key caps it
-- at one alert per item, kind, value and day, so a flapping price stays quiet.
CREATE TABLE alerts (
    id           INTEGER PRIMARY KEY,
    item_id      INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,        -- price_drop, sale_started, back_in_stock, my_size_back
    dedupe_key   TEXT NOT NULL UNIQUE, -- item:42|kind:price_drop|to:17300|day:2026-09-09
    payload_json TEXT,                 -- kind-specific detail: prices, size
    created_at   TEXT NOT NULL,
    notified_at  TEXT,                 -- null until the push has gone out
    read_at      TEXT                  -- null until acknowledged
);

CREATE INDEX idx_alerts_item_time ON alerts(item_id, created_at DESC);

-- Serves the watcher's retry query: what still needs sending.
CREATE INDEX idx_alerts_pending ON alerts(created_at) WHERE notified_at IS NULL;

-- +goose Down

DROP INDEX idx_alerts_pending;
DROP INDEX idx_alerts_item_time;
DROP TABLE alerts;
