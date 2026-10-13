-- Narrative claim chunks use the narrative's occurred time when present, so imported
-- history reads in the order the work happened rather than the order it was saved.
DROP TRIGGER work_narrative_claims_index;

CREATE TRIGGER work_narrative_claims_index AFTER INSERT ON work_narrative_claims
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    SELECT n.title, CASE WHEN NEW.label = '' THEN NEW.text ELSE NEW.label || ': ' || NEW.text END,
        NEW.project_id, 'narrative_claim', 'narrative:' || NEW.narrative_id || '#' || NEW.ordinal, NEW.kind,
        COALESCE(n.occurred_from, n.recorded_at)
    FROM work_narratives n WHERE n.project_id = NEW.project_id AND n.id = NEW.narrative_id;
END;

-- Rebuild derived narrative chunks; the claim insert trigger re-indexes them.
DELETE FROM history_chunks WHERE family = 'narrative_claim';
DELETE FROM work_narrative_claims;
INSERT INTO work_narrative_claims(project_id, narrative_id, ordinal, kind, label, text, authority)
SELECT n.project_id, n.id, CAST(c.key AS INTEGER), json_extract(c.value, '$.kind'),
    COALESCE(json_extract(c.value, '$.label'), ''), json_extract(c.value, '$.text'), json_extract(c.value, '$.authority')
FROM work_narratives n, json_each(n.payload_json, '$.claims') c;
