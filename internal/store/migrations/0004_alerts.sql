-- Clients report their time zone; schedules run in it.
ALTER TABLE clients ADD COLUMN timezone TEXT NOT NULL DEFAULT '';
-- When the server last lost contact with the client (0 = reachable).
ALTER TABLE clients ADD COLUMN unreachable_since INTEGER NOT NULL DEFAULT 0;

-- Alerts the server decided to send, and whether the email went out. key
-- makes each alert happen once (per run, per missed time, per outage).
CREATE TABLE alerts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL,
    client_id  TEXT NOT NULL DEFAULT '',
    job_id     TEXT NOT NULL DEFAULT '',
    run_id     TEXT NOT NULL DEFAULT '',
    subject    TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    sent_at    INTEGER NOT NULL DEFAULT 0,
    error      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX alerts_recent ON alerts (id DESC);
