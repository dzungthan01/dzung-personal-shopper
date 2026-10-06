-- +goose Up

-- watcher_runs is one row per polling pass, so a watcher that stopped working
-- shows up as a gap or a run of failures. finished_at stays NULL while a pass
-- runs, and forever for one that died mid-pass.
CREATE TABLE watcher_runs (
    id            INTEGER PRIMARY KEY,
    started_at    TEXT NOT NULL,
    finished_at   TEXT,
    items_checked INTEGER NOT NULL DEFAULT 0,
    items_failed  INTEGER NOT NULL DEFAULT 0,
    error_summary TEXT                  -- short and single-line; NULL when the pass succeeded
);

CREATE INDEX idx_watcher_runs_started ON watcher_runs(started_at);

-- +goose Down

DROP INDEX idx_watcher_runs_started;
DROP TABLE watcher_runs;
