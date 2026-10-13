package timeline

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/workstate"
)

const DefaultLimit = 20
const MaxLimit = 100

type Options struct {
	From, To, Kind, RoadmapNodeID, TaskID, Actor, SubjectRef, Cursor string
	Limit                                                            int
}

type Entry struct {
	Kind          string `json:"kind"`
	SourceRef     string `json:"source_ref"`
	SubjectRef    string `json:"subject_ref"`
	RoadmapNodeID string `json:"roadmap_node_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	OccurredAt    string `json:"occurred_at,omitempty"`
	RecordedAt    string `json:"recorded_at"`
	Actor         string `json:"actor,omitempty"`
	Producer      string `json:"producer,omitempty"`
	Authority     string `json:"authority"`
	Transition    string `json:"transition,omitempty"`
	Summary       string `json:"summary"`
}

type Snapshot struct {
	CurrentTaskID       string `json:"current_task_id,omitempty"`
	CurrentCheckpointID string `json:"current_checkpoint_id,omitempty"`
	CurrentStatus       string `json:"current_status,omitempty"`
	Next                string `json:"next,omitempty"`
	Blocked             int    `json:"blocked"`
	Stale               int    `json:"stale"`
	Ready               int    `json:"ready"`
	RoutingDecision     string `json:"routing_decision"`
}

type Page struct {
	SchemaVersion int       `json:"schema_version"`
	ProjectID     string    `json:"project_id"`
	Kinds         []string  `json:"kinds"`
	Snapshot      *Snapshot `json:"snapshot,omitempty"`
	Entries       []Entry   `json:"entries"`
	Limit         int       `json:"limit"`
	NextCursor    string    `json:"next_cursor,omitempty"`
	Authority     string    `json:"authority"`
	Disclaimer    string    `json:"disclaimer"`
}

type workReader interface {
	Summary(context.Context, string, int) (workstate.Summary, error)
	List(context.Context, string, string, int) (workstate.List, error)
}
type Service struct {
	DB   *sql.DB
	Work workReader
}

type cursor struct {
	Version int    `json:"v"`
	Filter  string `json:"f"`
	Key     string `json:"k"`
	Kind    string `json:"t"`
	Ref     string `json:"r"`
}

var kinds = []string{"activity_event", "checkpoint", "roadmap_revision", "work_item", "task_transition"}
var defaultKinds = []string{"checkpoint", "roadmap_revision", "work_item", "task_transition"}

func (s Service) List(ctx context.Context, projectID string, o Options) (Page, error) {
	if o.Limit == 0 {
		o.Limit = DefaultLimit
	}
	if o.Limit < 1 || o.Limit > MaxLimit {
		return Page{}, fmt.Errorf("timeline limit must be between 1 and %d", MaxLimit)
	}
	if o.Kind != "" {
		valid := false
		for _, kind := range kinds {
			if kind == o.Kind {
				valid = true
			}
		}
		if !valid {
			return Page{}, fmt.Errorf("unknown timeline kind %q", o.Kind)
		}
	}
	for _, value := range []string{o.RoadmapNodeID, o.TaskID, o.Actor, o.SubjectRef} {
		if len(value) > 256 {
			return Page{}, fmt.Errorf("timeline filter exceeds 256 bytes")
		}
	}
	from, err := parseTime(o.From)
	if err != nil {
		return Page{}, fmt.Errorf("from: %w", err)
	}
	to, err := parseTime(o.To)
	if err != nil {
		return Page{}, fmt.Errorf("to: %w", err)
	}
	if from != "" && to != "" && from > to {
		return Page{}, fmt.Errorf("timeline from must not exceed to")
	}
	filterBytes, _ := json.Marshal([]string{projectID, from, to, o.Kind, o.RoadmapNodeID, o.TaskID, o.Actor, o.SubjectRef})
	filterHash := sha256.Sum256(filterBytes)
	fingerprint := hex.EncodeToString(filterHash[:])
	var after cursor
	if o.Cursor != "" {
		if len(o.Cursor) > 2048 {
			return Page{}, fmt.Errorf("timeline cursor too long")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(o.Cursor)
		if err != nil {
			return Page{}, fmt.Errorf("invalid timeline cursor")
		}
		if err := json.Unmarshal(decoded, &after); err != nil || after.Version != 1 || after.Filter != fingerprint || after.Key == "" || after.Kind == "" || after.Ref == "" {
			return Page{}, fmt.Errorf("timeline cursor does not match filters")
		}
	}
	selectedKinds := defaultKinds
	if o.Kind != "" {
		selectedKinds = []string{o.Kind}
	}
	page := Page{SchemaVersion: 1, ProjectID: projectID, Kinds: append([]string(nil), selectedKinds...), Entries: []Entry{}, Limit: o.Limit, Authority: "mixed", Disclaimer: "This read-only timeline contains the listed saved record kinds. Actor and producer fields are claims; timestamp order does not establish causality. It does not select, repair, or execute work."}
	if o.Cursor == "" && s.Work != nil {
		work, err := s.Work.Summary(ctx, projectID, 5)
		if err != nil {
			return Page{}, err
		}
		blocked, err := s.Work.List(ctx, projectID, "blocked", 1)
		if err != nil {
			return Page{}, err
		}
		stale, err := s.Work.List(ctx, projectID, "stale", 1)
		if err != nil {
			return Page{}, err
		}
		snapshot := &Snapshot{Next: work.RecommendedNext, Blocked: blocked.InventoryTotal, Stale: stale.InventoryTotal, Ready: work.Counts.Ready, RoutingDecision: work.RoutingDecision}
		if work.Checkpoint != nil {
			snapshot.CurrentTaskID = work.Checkpoint.TaskID
			snapshot.CurrentCheckpointID = work.Checkpoint.ID
			snapshot.CurrentStatus = work.Checkpoint.Status
		}
		page.Snapshot = snapshot
	}
	rows, err := s.DB.QueryContext(ctx, query, projectID, from, to, o.Kind, o.RoadmapNodeID, o.TaskID, o.Actor, o.SubjectRef, after.Key, after.Kind, after.Ref, o.Limit+1)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	last := cursor{}
	hasMore := false
	for rows.Next() {
		var e Entry
		var key string
		if err := rows.Scan(&e.Kind, &e.SourceRef, &e.SubjectRef, &e.RoadmapNodeID, &e.TaskID, &e.OccurredAt, &e.RecordedAt, &e.Actor, &e.Producer, &e.Authority, &e.Transition, &e.Summary, &key); err != nil {
			return Page{}, err
		}
		if len(page.Entries) == o.Limit {
			hasMore = true
			break
		}
		e.Summary = concise(e.Summary)
		page.Entries = append(page.Entries, e)
		last = cursor{Version: 1, Filter: fingerprint, Key: key, Kind: e.Kind, Ref: e.SourceRef}
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if hasMore {
		encoded, err := json.Marshal(last)
		if err != nil {
			return Page{}, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return page, nil
}

func concise(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 160 {
		return string(runes[:160]) + "…"
	}
	return value
}

func parseTime(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", err
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z"), nil
}

const query = `WITH items AS (
 SELECT 'activity_event' kind, 'event:'||id ref, 'event:'||id subject, '' node, coalesce(task_id,'') task, occurred_at occurred, received_at recorded, actor_type actor, client producer, 'machine_observed' authority, event_type transition, category summary FROM events WHERE project_id=?1
 UNION ALL SELECT 'checkpoint', 'checkpoint:'||id, 'checkpoint:'||id, '', coalesce(task_id,''), created_at, created_at, 'caller', '', 'caller_asserted', status, task_title FROM checkpoints WHERE project_id=?1
 UNION ALL SELECT 'roadmap_revision', 'node:'||node_id||'@'||revision, 'node:'||node_id, node_id, '', recorded_at, recorded_at, 'caller', '', authority, status, title FROM roadmap_nodes WHERE project_id=?1
 UNION ALL SELECT 'work_item', 'work_item:'||id, 'work_item:'||id, coalesce(roadmap_node_id,''), coalesce(task_id,''), recorded_at, recorded_at, creator, '', authority, kind, title FROM work_items WHERE project_id=?1
 UNION ALL SELECT 'task_transition', 'task-transition:'||id, 'task:'||task_id, '', task_id, created_at, created_at, 'caller', '', 'caller_asserted', operation, coalesce(reason,operation) FROM task_lineage_events WHERE project_id=?1
), keyed AS (
 SELECT *, strftime('%Y-%m-%dT%H:%M:%fZ', recorded) k FROM items
)
SELECT kind,ref,subject,node,task,occurred,recorded,actor,producer,authority,transition,summary,k FROM keyed
WHERE (?2='' OR k>=?2) AND (?3='' OR k<=?3) AND ((?4='' AND kind<>'activity_event') OR kind=?4)
 AND (?5='' OR node=?5) AND (?6='' OR task=?6) AND (?7='' OR actor=?7 OR producer=?7)
 AND (?8='' OR subject=?8)
 AND (?9='' OR k<?9 OR (k=?9 AND (kind>?10 OR (kind=?10 AND ref>?11))))
ORDER BY k DESC, kind ASC, ref ASC LIMIT ?12`
