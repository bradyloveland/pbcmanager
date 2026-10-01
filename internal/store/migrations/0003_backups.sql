-- PBS datastores. secret is encrypted.
CREATE TABLE destinations (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    host        TEXT NOT NULL,
    port        INTEGER NOT NULL,
    datastore   TEXT NOT NULL,
    namespace   TEXT NOT NULL DEFAULT '',
    username    TEXT NOT NULL,
    token_name  TEXT NOT NULL DEFAULT '',
    secret      TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

-- Backup jobs. shares, excludes, schedule and destinations are JSON;
-- keyfile_password is encrypted.
CREATE TABLE jobs (
    id               TEXT PRIMARY KEY,
    client_id        TEXT NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    backup_id        TEXT NOT NULL,
    shares           TEXT NOT NULL,
    excludes         TEXT NOT NULL,
    schedule         TEXT NOT NULL,
    change_detection TEXT NOT NULL,
    rate             TEXT NOT NULL DEFAULT '',
    keyfile          TEXT NOT NULL DEFAULT '',
    keyfile_password TEXT NOT NULL DEFAULT '',
    enabled          INTEGER NOT NULL,
    destinations     TEXT NOT NULL,
    created_at       INTEGER NOT NULL,
    UNIQUE (client_id, name)
);

-- Runs collected from clients. Run IDs are made on the client.
CREATE TABLE runs (
    client_id        TEXT NOT NULL,
    id               TEXT NOT NULL,
    job_id           TEXT NOT NULL,
    group_id         TEXT NOT NULL,
    job_name         TEXT NOT NULL,
    destination_id   TEXT NOT NULL,
    destination_name TEXT NOT NULL,
    trigger          TEXT NOT NULL,
    status           TEXT NOT NULL,
    started          INTEGER NOT NULL,
    ended            INTEGER NOT NULL,
    exit_code        INTEGER,
    summary          TEXT NOT NULL,
    log_size         INTEGER NOT NULL,
    updated          INTEGER NOT NULL,
    collected_at     INTEGER NOT NULL,
    log_saved        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (client_id, id)
);
CREATE INDEX runs_job ON runs (job_id, id DESC);
CREATE INDEX runs_recent ON runs (id DESC);

ALTER TABLE clients ADD COLUMN applied_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE clients ADD COLUMN apply_error TEXT NOT NULL DEFAULT '';
ALTER TABLE clients ADD COLUMN run_cursor INTEGER NOT NULL DEFAULT 0;
