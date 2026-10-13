ALTER TABLE roadmap_nodes ADD COLUMN kind TEXT NOT NULL DEFAULT 'work_item';
ALTER TABLE roadmap_nodes ADD COLUMN target_date TEXT;
ALTER TABLE roadmap_nodes ADD COLUMN target_release TEXT;
ALTER TABLE roadmap_nodes ADD COLUMN target_horizon TEXT;
ALTER TABLE roadmap_nodes ADD COLUMN evidence_refs_json TEXT NOT NULL DEFAULT '[]';
