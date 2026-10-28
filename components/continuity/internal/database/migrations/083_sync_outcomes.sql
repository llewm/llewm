-- Preserve observed outcomes for status readers, including the terminal UI.
-- Old attempts remain explicitly unclassified; zero records does not prove
-- the unchanged fast path was used.
ALTER TABLE sync_attempts ADD COLUMN unchanged INTEGER CHECK(unchanged IN (0,1));
ALTER TABLE sync_attempts ADD COLUMN forks INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_attempts ADD COLUMN supersession_forks INTEGER NOT NULL DEFAULT 0;
