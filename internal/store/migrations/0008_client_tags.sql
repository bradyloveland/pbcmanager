-- Tags the user gives a client, to group clients and their jobs (a JSON
-- array of strings). Written only by SetClientTags, never by SaveClient, so a
-- check-in can't undo an edit made at the same time.
ALTER TABLE clients ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';
