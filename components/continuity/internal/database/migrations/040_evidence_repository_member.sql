-- In a unified workspace, a commit belongs to one member repository. The member
-- path is provenance kept beside the record: it is not part of the hashed payload,
-- so portable round trips of existing records are unchanged.
ALTER TABLE evidence_records ADD COLUMN repository_member TEXT;
