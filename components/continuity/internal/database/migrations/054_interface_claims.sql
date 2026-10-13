-- Interface changes (MCP API version, CLI commands, migrations) become their own
-- claim kind instead of outcome claims labeled Interface. Claim rows are derived
-- from work_narratives, so the table is rebuilt with the wider kind check and
-- backfilled; the claim insert trigger re-indexes the chunks.
DELETE FROM history_chunks WHERE family = 'narrative_claim';
DROP TABLE work_narrative_claims;

CREATE TABLE work_narrative_claims (
    project_id TEXT NOT NULL,
    narrative_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    kind TEXT NOT NULL CHECK (kind IN ('intent', 'outcome', 'interface', 'constraint', 'alternative', 'verification', 'decision', 'next_action', 'limitation', 'note')),
    label TEXT NOT NULL,
    text TEXT NOT NULL,
    authority TEXT NOT NULL,
    PRIMARY KEY(project_id, narrative_id, ordinal),
    FOREIGN KEY(project_id, narrative_id) REFERENCES work_narratives(project_id, id)
) STRICT;

CREATE INDEX work_narrative_claims_kind
    ON work_narrative_claims(project_id, kind, narrative_id, ordinal);

CREATE TRIGGER work_narrative_claims_index AFTER INSERT ON work_narrative_claims
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    SELECT n.title, CASE WHEN NEW.label = '' THEN NEW.text ELSE NEW.label || ': ' || NEW.text END,
        NEW.project_id, 'narrative_claim', 'narrative:' || NEW.narrative_id || '#' || NEW.ordinal, NEW.kind,
        COALESCE(n.occurred_from, n.recorded_at)
    FROM work_narratives n WHERE n.project_id = NEW.project_id AND n.id = NEW.narrative_id;
END;

INSERT INTO work_narrative_claims(project_id, narrative_id, ordinal, kind, label, text, authority)
SELECT n.project_id, n.id, CAST(c.key AS INTEGER), json_extract(c.value, '$.kind'),
    COALESCE(json_extract(c.value, '$.label'), ''), json_extract(c.value, '$.text'), json_extract(c.value, '$.authority')
FROM work_narratives n, json_each(n.payload_json, '$.claims') c;
