-- How far a running backup has got (bundle.Progress as JSON). Empty once
-- the run has ended, and for runs from before 2.3.0.
ALTER TABLE runs ADD COLUMN progress TEXT NOT NULL DEFAULT '';
