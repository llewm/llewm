package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/release"
)

type releasePayload struct {
	Version                  string          `json:"version"`
	Title                    string          `json:"title"`
	Status                   string          `json:"status"`
	Reviewer                 string          `json:"reviewer,omitempty"`
	PublishedAt              string          `json:"published_at,omitempty"`
	ContentSHA256            string          `json:"content_sha256"`
	Entries                  []release.Entry `json:"entries"`
	OperationalSchemaVersion int             `json:"operational_schema_version"`
}

func loadReleaseSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	rows, err := exportRows(ctx, source, "roadmap_releases", `SELECT release_id,revision,version,title,status,COALESCE(reviewer,''),published_at,content_sha256,authority,recorded_at,schema_version FROM roadmap_releases WHERE project_id=? ORDER BY release_id,revision`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	specs := []EnvelopeSpec{}
	for rows.Next() {
		var value release.Record
		var published sql.NullString
		var recorded string
		if err := rows.Scan(&value.ID, &value.Revision, &value.Version, &value.Title, &value.Status, &value.Reviewer, &published, &value.ContentSHA256, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			return nil, err
		}
		value.ProjectID = projectID
		value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			return nil, err
		}
		if published.Valid {
			parsed, parseErr := time.Parse(time.RFC3339Nano, published.String)
			if parseErr != nil {
				return nil, parseErr
			}
			value.PublishedAt = &parsed
		}
		entryRows, queryErr := source.QueryContext(ctx, `SELECT position,category,wording,node_id,node_revision,narrative_ids_json FROM roadmap_release_entries WHERE project_id=? AND release_id=? AND release_revision=? ORDER BY position`, projectID, value.ID, value.Revision)
		if queryErr != nil {
			return nil, queryErr
		}
		for entryRows.Next() {
			var entry release.Entry
			var narratives string
			if err := entryRows.Scan(&entry.Position, &entry.Category, &entry.Wording, &entry.NodeID, &entry.NodeRevision, &narratives); err != nil {
				entryRows.Close()
				return nil, err
			}
			if err := json.Unmarshal([]byte(narratives), &entry.NarrativeIDs); err != nil {
				entryRows.Close()
				return nil, err
			}
			value.Entries = append(value.Entries, entry)
		}
		if err := entryRows.Err(); err != nil {
			entryRows.Close()
			return nil, err
		}
		if err := entryRows.Close(); err != nil {
			return nil, err
		}
		payload := releasePayload{Version: portableText(value.Version, projectRoot), Title: portableText(value.Title, projectRoot), Status: value.Status, Reviewer: portableText(value.Reviewer, projectRoot), ContentSHA256: value.ContentSHA256, Entries: value.Entries, OperationalSchemaVersion: value.SchemaVersion}
		if value.PublishedAt != nil {
			payload.PublishedAt = value.PublishedAt.UTC().Format(time.RFC3339Nano)
		}
		for i := range payload.Entries {
			payload.Entries[i].Wording = portableText(payload.Entries[i].Wording, projectRoot)
		}
		portableValue := release.Record{Version: payload.Version, Title: payload.Title, Entries: payload.Entries}
		payload.ContentSHA256, err = release.ContentHash(portableValue)
		if err != nil {
			return nil, err
		}
		body, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, marshalErr
		}
		index, _ := json.Marshal(envelopeIndex{Title: payload.Version, Status: payload.Status})
		refs := []Reference{}
		for _, entry := range payload.Entries {
			refs = appendUniqueReference(refs, Reference{Kind: "roadmap_node_revision", ID: roadmapRevisionID(entry.NodeID, entry.NodeRevision)})
			for _, id := range entry.NarrativeIDs {
				refs = appendUniqueReference(refs, Reference{Kind: "work_narrative", ID: id})
			}
		}
		specs = append(specs, EnvelopeSpec{LineageID: lineageID, RecordType: "roadmap_release", RecordSchemaVersion: 1, RecordID: value.ID, RecordRevision: value.Revision, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: refs, Payload: body})
	}
	return specs, rows.Err()
}

func decodeRelease(envelope Envelope, payload releasePayload) (release.Record, error) {
	if envelope.RecordSchemaVersion != 1 || payload.OperationalSchemaVersion != release.SchemaVersion {
		return release.Record{}, fmt.Errorf("roadmap release %s/%d has incompatible schema", envelope.RecordID, envelope.RecordRevision)
	}
	if payload.Version == "" || payload.Title == "" || !portableHashPattern.MatchString(payload.ContentSHA256) || len(payload.Entries) == 0 || len(payload.Entries) > release.MaxEntries {
		return release.Record{}, fmt.Errorf("roadmap release %s/%d is incomplete", envelope.RecordID, envelope.RecordRevision)
	}
	if !oneOfValue(payload.Status, "draft", "reviewed", "published", "superseded") || (envelope.RecordID == "unreleased" && payload.Status != "draft") {
		return release.Record{}, fmt.Errorf("roadmap release %s/%d has invalid status", envelope.RecordID, envelope.RecordRevision)
	}
	var published *time.Time
	if payload.PublishedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, payload.PublishedAt)
		if err != nil {
			return release.Record{}, err
		}
		published = &parsed
	}
	for i, entry := range payload.Entries {
		if entry.Position != i+1 || !oneOfValue(entry.Category, "feature", "fix", "migration", "operational", "breaking") || entry.Wording == "" || entry.NodeID == "" || entry.NodeRevision < 1 || len(entry.NarrativeIDs) == 0 || len(entry.NarrativeIDs) > release.MaxNarrativesPerEntry {
			return release.Record{}, fmt.Errorf("roadmap release %s/%d has invalid entry %d", envelope.RecordID, envelope.RecordRevision, i+1)
		}
	}
	recorded, err := time.Parse(time.RFC3339Nano, envelope.RecordedAt)
	if err != nil {
		return release.Record{}, err
	}
	value := release.Record{ID: envelope.RecordID, Revision: envelope.RecordRevision, Version: payload.Version, Title: payload.Title, Status: payload.Status, Reviewer: payload.Reviewer, PublishedAt: published, ContentSHA256: payload.ContentSHA256, Entries: payload.Entries, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}
	hash, err := release.ContentHash(value)
	if err != nil || hash != value.ContentSHA256 {
		return release.Record{}, fmt.Errorf("roadmap release %s/%d content hash mismatch", envelope.RecordID, envelope.RecordRevision)
	}
	return value, nil
}

func appendReleaseRecords(ctx context.Context, tx *sql.Tx, projectID string, values map[string]release.Record, appendKeys map[string]struct{}) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := values[key]
		if _, ok := appendKeys[identityKey("roadmap_release", value.ID, value.Revision)]; !ok {
			continue
		}
		var published any
		if value.PublishedAt != nil {
			published = value.PublishedAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_releases(project_id,release_id,revision,version,title,status,reviewer,published_at,content_sha256,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,NULLIF(?,''),?,?,?,?,?)`, projectID, value.ID, value.Revision, value.Version, value.Title, value.Status, value.Reviewer, published, value.ContentSHA256, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion); err != nil {
			return err
		}
		for _, entry := range value.Entries {
			narratives, _ := json.Marshal(entry.NarrativeIDs)
			if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_release_entries(project_id,release_id,release_revision,position,category,wording,node_id,node_revision,narrative_ids_json) VALUES(?,?,?,?,?,?,?,?,?)`, projectID, value.ID, value.Revision, entry.Position, entry.Category, entry.Wording, entry.NodeID, entry.NodeRevision, string(narratives)); err != nil {
				return err
			}
		}
	}
	return nil
}
