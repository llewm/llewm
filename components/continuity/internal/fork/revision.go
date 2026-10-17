package fork

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FamilyRevision is one record revision that machines wrote with different
// content before syncing.
const FamilyRevision = "revision"

// RevisionHead is one version of a forked record revision.
type RevisionHead struct {
	ContentSHA256  string    `json:"content_sha256"`
	EnvelopeSHA256 string    `json:"envelope_sha256"`
	RecordedAt     time.Time `json:"recorded_at"`
	// Local is the version this project holds: its own, or the one sync
	// imported when the project had none.
	Local    bool   `json:"local"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status,omitempty"`
	envelope []byte
}

// RevisionFork is one record revision with more than one version. Heads are
// newest first (envelope hash breaks ties); the first is the proposed head.
type RevisionFork struct {
	RecordType string         `json:"record_type"`
	RecordID   string         `json:"record_id"`
	Revision   int            `json:"revision"`
	Heads      []RevisionHead `json:"heads"`
}

// Ref is the fork's reference, as `fork resolve` takes it.
func (f RevisionFork) Ref() string { return RevisionSubject(f.RecordType, f.RecordID, f.Revision) }

// Proposed is the head a resolution would accept by default.
func (f RevisionFork) Proposed() RevisionHead { return f.Heads[0] }

// RevisionSubject names one record revision as TYPE:ID@REVISION.
func RevisionSubject(recordType, recordID string, revision int) string {
	return fmt.Sprintf("%s:%s@%d", recordType, recordID, revision)
}

// ParseRevisionSubject splits TYPE:ID@REVISION. Record types never contain a
// colon, so the first colon ends the type and the last @ starts the revision.
func ParseRevisionSubject(value string) (string, string, int, error) {
	recordType, rest, ok := strings.Cut(strings.TrimSpace(value), ":")
	at := strings.LastIndex(rest, "@")
	if !ok || recordType == "" || at <= 0 {
		return "", "", 0, fmt.Errorf("name a revision fork as TYPE:ID@REVISION, not %q", value)
	}
	revision, err := strconv.Atoi(rest[at+1:])
	if err != nil || revision < 1 {
		return "", "", 0, fmt.Errorf("revision fork %q needs a revision of at least 1", value)
	}
	return recordType, rest[:at], revision, nil
}

// ObservedHead is one version of a record revision as sync found it: the
// canonical envelope line and whether the project holds it.
type ObservedHead struct {
	RecordType     string
	RecordID       string
	Revision       int
	EnvelopeSHA256 string
	ContentSHA256  string
	Envelope       []byte
	RecordedAt     time.Time
	Local          bool
}

// RecordHeads keeps every version of forked record revisions. Versions seen
// before are left as they are, so repeated syncs change nothing.
func RecordHeads(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, projectID string, heads []ObservedHead, at time.Time) error {
	for _, head := range heads {
		if head.Revision < 1 || head.ContentSHA256 == "" || head.EnvelopeSHA256 == "" || len(head.Envelope) == 0 {
			return fmt.Errorf("fork head %s needs a revision, content hash, envelope hash, and envelope", RevisionSubject(head.RecordType, head.RecordID, head.Revision))
		}
		local := 0
		if head.Local {
			local = 1
		}
		args := []any{projectID, head.RecordType, head.RecordID, head.Revision, head.ContentSHA256, head.EnvelopeSHA256, local, string(head.Envelope), head.RecordedAt.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano)}
		if _, err := db.ExecContext(ctx, `INSERT INTO fork_head_observations(project_id,record_type,record_id,record_revision,content_sha256,envelope_sha256,is_local,envelope_json,recorded_at,observed_at,authority,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,'machine_observed',1) ON CONFLICT(project_id,envelope_sha256) DO NOTHING`, args...); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO fork_heads(project_id,record_type,record_id,record_revision,content_sha256,envelope_sha256,is_local,envelope_json,recorded_at,observed_at,authority,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,'machine_observed',1) ON CONFLICT(project_id,record_type,record_id,record_revision,content_sha256) DO NOTHING`, args...); err != nil {
			return err
		}
	}
	return nil
}

// HeadEnvelope returns the stored envelope line of one fork head.
func HeadEnvelope(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, recordType, recordID string, revision int, contentSHA256 string) ([]byte, error) {
	var body string
	if err := db.QueryRowContext(ctx, `SELECT envelope_json FROM fork_heads WHERE project_id=? AND record_type=? AND record_id=? AND record_revision=? AND content_sha256=?`, projectID, recordType, recordID, revision, contentSHA256).Scan(&body); err != nil {
		return nil, err
	}
	return []byte(body), nil
}

// RevisionForks returns every record revision (or only subject, TYPE:ID@REVISION,
// when set) with more than one version that no resolution rejected.
func RevisionForks(ctx context.Context, db queryer, projectID, subject string) ([]RevisionFork, error) {
	recordType, recordID, revision := "", "", 0
	if subject != "" {
		var err error
		if recordType, recordID, revision, err = ParseRevisionSubject(subject); err != nil {
			return nil, err
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT record_type,record_id,record_revision,content_sha256,envelope_sha256,is_local,envelope_json,recorded_at FROM fork_heads
        WHERE project_id=? AND (?='' OR (record_type=? AND record_id=? AND record_revision=?))
		AND NOT EXISTS (SELECT 1 FROM redaction_recoveries r WHERE r.project_id=fork_heads.project_id AND r.record_type=fork_heads.record_type AND r.record_id=fork_heads.record_id AND r.record_revision=fork_heads.record_revision AND r.derived_content_sha256=fork_heads.content_sha256)
        ORDER BY record_type,record_id,record_revision,envelope_sha256`, projectID, subject, recordType, recordID, revision)
	if err != nil {
		return nil, err
	}
	var forks []RevisionFork
	for rows.Next() {
		var head RevisionHead
		var value RevisionFork
		var local int
		var envelope, recorded string
		if err := rows.Scan(&value.RecordType, &value.RecordID, &value.Revision, &head.ContentSHA256, &head.EnvelopeSHA256, &local, &envelope, &recorded); err != nil {
			rows.Close()
			return nil, err
		}
		head.Local = local == 1
		if head.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			rows.Close()
			return nil, err
		}
		var summary struct {
			Index struct {
				Title  string `json:"title"`
				Status string `json:"status"`
			} `json:"index"`
		}
		_ = json.Unmarshal([]byte(envelope), &summary) // stored envelopes were decoded before; a label is optional
		head.Title, head.Status = summary.Index.Title, summary.Index.Status
		head.envelope = []byte(envelope)
		if last := len(forks) - 1; last >= 0 && forks[last].Ref() == value.Ref() {
			forks[last].Heads = append(forks[last].Heads, head)
		} else {
			value.Heads = []RevisionHead{head}
			forks = append(forks, value)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	rejected, err := rejectedRevisionHeads(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	open := forks[:0]
	for _, value := range forks {
		heads := value.Heads[:0]
		for _, head := range value.Heads {
			if !rejected[head.ContentSHA256] {
				heads = append(heads, head)
			}
		}
		if len(heads) < 2 || equivalentRoadmapNodeRevisionHeads(heads) {
			continue
		}
		sort.Slice(heads, func(i, j int) bool {
			if !heads[i].RecordedAt.Equal(heads[j].RecordedAt) {
				return heads[i].RecordedAt.After(heads[j].RecordedAt)
			}
			return heads[i].ContentSHA256 > heads[j].ContentSHA256
		})
		value.Heads = heads
		open = append(open, value)
	}
	return open, nil
}

// EquivalentRoadmapNodeEnvelopes reports the one schema migration that cannot
// represent a semantic fork: v1 and v2 roadmap-node envelopes whose normalized
// payloads are identical. Raw observed heads remain stored for audit.
func EquivalentRoadmapNodeEnvelopes(envelopes [][]byte) bool {
	if len(envelopes) < 2 {
		return false
	}
	var expected []byte
	for _, body := range envelopes {
		var envelope struct {
			RecordType string         `json:"record_type"`
			Schema     int            `json:"record_schema_version"`
			Payload    map[string]any `json:"payload"`
		}
		if json.Unmarshal(body, &envelope) != nil || envelope.RecordType != "roadmap_node" || envelope.Schema < 1 || envelope.Schema > 2 {
			return false
		}
		if envelope.Payload["kind"] == "work_item" {
			delete(envelope.Payload, "kind")
		}
		if refs, ok := envelope.Payload["evidence_refs"].([]any); ok && len(refs) == 0 {
			delete(envelope.Payload, "evidence_refs")
		}
		normalized, err := json.Marshal(envelope.Payload)
		if err != nil {
			return false
		}
		if expected == nil {
			expected = normalized
			continue
		}
		if string(expected) != string(normalized) {
			return false
		}
	}
	return true
}

func equivalentRoadmapNodeRevisionHeads(heads []RevisionHead) bool {
	envelopes := make([][]byte, len(heads))
	for index := range heads {
		envelopes[index] = heads[index].envelope
	}
	return EquivalentRoadmapNodeEnvelopes(envelopes)
}
