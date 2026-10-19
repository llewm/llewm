-- Source observations remain canonical work narratives. Citation rows are a
-- rebuildable graph projection; the deferred FK supports portable batch import.
CREATE TABLE work_narrative_citations (
 project_id TEXT NOT NULL,
 narrative_id TEXT NOT NULL,
 claim_ordinal INTEGER NOT NULL,
 ordinal INTEGER NOT NULL,
 source_id TEXT NOT NULL,
 relation TEXT NOT NULL CHECK (relation IN ('supports','contradicts','context')),
 locator TEXT NOT NULL,
 PRIMARY KEY(project_id,narrative_id,claim_ordinal,ordinal),
 FOREIGN KEY(project_id,narrative_id) REFERENCES work_narratives(project_id,id),
 FOREIGN KEY(project_id,source_id) REFERENCES work_narratives(project_id,id) DEFERRABLE INITIALLY DEFERRED
) STRICT;
CREATE INDEX work_narrative_citations_source ON work_narrative_citations(project_id,source_id,narrative_id,claim_ordinal);
CREATE INDEX work_narrative_source_url ON work_narratives(project_id,json_extract(payload_json,'$.source.url'));
CREATE TRIGGER work_narratives_citations_insert AFTER INSERT ON work_narratives BEGIN
 INSERT INTO work_narrative_citations
 SELECT NEW.project_id,NEW.id,CAST(c.key AS INTEGER),CAST(e.key AS INTEGER),substr(json_extract(e.value,'$.source'),11),json_extract(e.value,'$.relation'),COALESCE(json_extract(e.value,'$.locator'),'')
 FROM json_each(NEW.payload_json,'$.claims') c,json_each(c.value,'$.citations') e;
END;

DROP TRIGGER work_narrative_claims_index;
CREATE TRIGGER work_narrative_claims_index AFTER INSERT ON work_narrative_claims BEGIN
 INSERT INTO history_chunks(title,body,project_id,family,ref,kind,recorded_at)
 SELECT n.title, CASE WHEN NEW.label='' THEN NEW.text ELSE NEW.label||': '||NEW.text END
 ||COALESCE(char(10)||json_extract(n.payload_json,'$.source.url'),'')
 ||COALESCE(char(10)||json_extract(n.payload_json,'$.source.publisher'),'')
 ||COALESCE(char(10)||json_extract(n.payload_json,'$.source.version'),''),
 NEW.project_id,'narrative_claim','narrative:'||NEW.narrative_id||'#'||NEW.ordinal,NEW.kind,COALESCE(n.occurred_from,n.recorded_at)
 FROM work_narratives n WHERE n.project_id=NEW.project_id AND n.id=NEW.narrative_id;
END;
