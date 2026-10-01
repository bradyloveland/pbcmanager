-- Settings: one JSON value per key.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- The single admin account. totp_secret is encrypted; recovery_codes is a
-- JSON list of SHA-256 hashes.
CREATE TABLE admin (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    username       TEXT NOT NULL,
    password_hash  TEXT NOT NULL,
    totp_secret    TEXT NOT NULL DEFAULT '',
    totp_last_step INTEGER NOT NULL DEFAULT 0,
    recovery_codes TEXT NOT NULL DEFAULT '[]',
    updated_at     INTEGER NOT NULL
);

-- Sessions are stored by the SHA-256 of their token, never the token itself.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    ip         TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT ''
);
