-- Cached sizes for the dashboard: destination space, latest backup size per
-- job and destination, and folder sizes measured on clients. data is JSON.
CREATE TABLE sizes (
    kind    TEXT NOT NULL,   -- dest, backup, folder
    key     TEXT NOT NULL,
    data    TEXT NOT NULL,
    updated INTEGER NOT NULL,
    PRIMARY KEY (kind, key)
);
