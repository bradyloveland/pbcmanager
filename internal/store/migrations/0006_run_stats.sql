-- A run's backup figures (bundle.Stats as JSON), read from
-- proxmox-backup-client's output. Empty for runs from before 2.1.0.
ALTER TABLE runs ADD COLUMN stats TEXT NOT NULL DEFAULT '';
