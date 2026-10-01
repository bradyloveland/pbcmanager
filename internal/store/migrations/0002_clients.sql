-- Machines the server manages over SSH. host_key is the pinned SSH host key
-- in authorized_keys format; no login password is ever stored.
CREATE TABLE clients (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    address         TEXT NOT NULL,
    port            INTEGER NOT NULL,
    host_key        TEXT NOT NULL,
    status          TEXT NOT NULL,       -- setting-up, ready, error, unreachable, host-key-changed
    status_detail   TEXT NOT NULL DEFAULT '',
    offered_key     TEXT NOT NULL DEFAULT '', -- a different host key the client presented, awaiting a decision
    os_id           TEXT NOT NULL DEFAULT '',
    os_pretty       TEXT NOT NULL DEFAULT '',
    os_codename     TEXT NOT NULL DEFAULT '',
    arch            TEXT NOT NULL DEFAULT '',
    hostname        TEXT NOT NULL DEFAULT '',
    systemd_version TEXT NOT NULL DEFAULT '',
    client_version  TEXT NOT NULL DEFAULT '',
    runner_version  TEXT NOT NULL DEFAULT '',
    server_here     INTEGER NOT NULL DEFAULT 0,
    last_contact    INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    UNIQUE (address, port)
);
