ALTER TABLE track_plan_revisions ADD COLUMN change_json TEXT;
ALTER TABLE track_plan_items ADD COLUMN progress_note TEXT NOT NULL DEFAULT '';
ALTER TABLE track_plan_items ADD COLUMN acceptance_criteria TEXT NOT NULL DEFAULT '';
