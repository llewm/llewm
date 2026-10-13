-- Coverage cells sync (7b of continuity-multi-repo-sync): each cell travels as
-- a portable coverage_cell record and names the instance (machine store) that
-- observed it. Cells recorded before this migration were observed here.
ALTER TABLE coverage_cells ADD COLUMN instance_id TEXT;

DROP TRIGGER coverage_cells_no_update;
UPDATE coverage_cells SET instance_id=(SELECT instance_id FROM project_memory_identities i WHERE i.project_id=coverage_cells.project_id);
CREATE TRIGGER coverage_cells_no_update BEFORE UPDATE ON coverage_cells BEGIN SELECT RAISE(ABORT,'coverage cells are append-only'); END;
