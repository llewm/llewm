// Package fork finds records with more than one current head, usually because
// two machines continued the same record before syncing, and stores the light
// resolutions that settle them. Detection orders heads by their parent links and
// proposes the newest (checkpoint ID breaks ties); it never picks one on its own.
// A resolution keeps every head in history.
package fork

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the original fork_resolutions row and portable payload version.
const SchemaVersion = 1

// RevisionSchemaVersion identifies revision resolutions whose heads are stable
// payload content hashes rather than portable envelope hashes.
const RevisionSchemaVersion = 2

// FamilyCheckpoint is a task whose checkpoints have several heads.
const FamilyCheckpoint = "checkpoint"

// Head is one current head of a fork.
type Head struct {
	CheckpointID string    `json:"checkpoint_id"`
	Task         string    `json:"task"`
	Status       string    `json:"status"`
	Summary      string    `json:"summary"`
	NextAction   string    `json:"next_action"`
	CreatedAt    time.Time `json:"created_at"`
	// Depth is how many recorded continuations lead to this head from the
	// oldest checkpoint with a context.
	Depth int `json:"depth"`
}

// Fork is one task with more than one head. Heads are newest first; the first
// is the proposed head.
type Fork struct {
	Family  string `json:"family"`
	Subject string `json:"subject"`
	// Base is the newest checkpoint every head descends from, when there is one.
	Base  string `json:"base,omitempty"`
	Heads []Head `json:"heads"`
}

// Ref is the fork's reference, as `fork resolve` takes it.
func (f Fork) Ref() string { return "task:" + f.Subject }

// Proposed is the head a resolution would accept by default.
func (f Fork) Proposed() Head { return f.Heads[0] }

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type node struct {
	head    Head
	basedOn string
}

// CheckpointForks returns every task (or only taskID when set) whose
// checkpoints have more than one head. A head is a checkpoint with a recorded
// context that no other checkpoint continues and no resolution rejected.
// Checkpoints written before contexts existed are not considered.
func CheckpointForks(ctx context.Context, db queryer, projectID, taskID string) ([]Fork, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.task_id, c.id, c.task_title, c.status, c.summary, c.next_action, c.created_at, COALESCE(x.based_on_checkpoint_id, '')
        FROM checkpoints c JOIN checkpoint_contexts x ON x.project_id = c.project_id AND x.checkpoint_id = c.id
        WHERE c.project_id = ? AND c.task_id IS NOT NULL AND (? = '' OR c.task_id = ?)
        ORDER BY c.task_id, c.created_at, c.id`, projectID, taskID, taskID)
	if err != nil {
		return nil, err
	}
	tasks := map[string]map[string]*node{}
	var order []string
	for rows.Next() {
		var task, created string
		var value node
		if err := rows.Scan(&task, &value.head.CheckpointID, &value.head.Task, &value.head.Status, &value.head.Summary, &value.head.NextAction, &created, &value.basedOn); err != nil {
			rows.Close()
			return nil, err
		}
		if value.head.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			rows.Close()
			return nil, err
		}
		if tasks[task] == nil {
			tasks[task] = map[string]*node{}
			order = append(order, task)
		}
		tasks[task][value.head.CheckpointID] = &value
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	rejected, err := rejectedHeads(ctx, db, projectID, FamilyCheckpoint)
	if err != nil {
		return nil, err
	}
	var forks []Fork
	for _, task := range order {
		nodes := tasks[task]
		continued := map[string]bool{}
		for _, value := range nodes {
			continued[value.basedOn] = true
		}
		var heads []Head
		for id, value := range nodes {
			if continued[id] || rejected[id] {
				continue
			}
			value.head.Depth = depth(nodes, id)
			heads = append(heads, value.head)
		}
		if len(heads) < 2 {
			continue
		}
		sort.Slice(heads, func(i, j int) bool {
			if !heads[i].CreatedAt.Equal(heads[j].CreatedAt) {
				return heads[i].CreatedAt.After(heads[j].CreatedAt)
			}
			return heads[i].CheckpointID > heads[j].CheckpointID
		})
		forks = append(forks, Fork{Family: FamilyCheckpoint, Subject: task, Base: commonBase(nodes, heads), Heads: heads})
	}
	return forks, nil
}

// depth counts recorded continuations back from id, stopping at a checkpoint
// without a context (or a cycle, which contexts cannot form but data could).
func depth(nodes map[string]*node, id string) int {
	count, seen := 0, map[string]bool{}
	for {
		value, ok := nodes[id]
		if !ok || seen[id] || value.basedOn == "" {
			return count
		}
		seen[id] = true
		id = value.basedOn
		count++
	}
}

// commonBase is the newest checkpoint every head descends from, or "".
func commonBase(nodes map[string]*node, heads []Head) string {
	ancestors := func(id string) []string {
		var chain []string
		seen := map[string]bool{}
		for id != "" && !seen[id] {
			seen[id] = true
			chain = append(chain, id)
			value, ok := nodes[id]
			if !ok {
				break
			}
			id = value.basedOn
		}
		return chain
	}
	counts := map[string]int{}
	for _, head := range heads {
		for _, id := range ancestors(head.CheckpointID) {
			counts[id]++
		}
	}
	// The first shared ancestor along any head's chain is the newest shared one.
	for _, id := range ancestors(heads[0].CheckpointID) {
		if counts[id] == len(heads) {
			return id
		}
	}
	return ""
}

// Resolution settles a fork: it names the accepted head, the rejected heads,
// and the continuation written from the accepted head.
type Resolution struct {
	ProjectID     string    `json:"project_id,omitempty"`
	ID            string    `json:"id"`
	Family        string    `json:"family"`
	Subject       string    `json:"subject_id"`
	AcceptedHead  string    `json:"accepted_head"`
	RejectedHeads []string  `json:"rejected_heads"`
	Continuation  string    `json:"continuation_id,omitempty"`
	Resolver      string    `json:"resolver"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

// Validate checks a resolution and returns it with rejected heads sorted.
func Validate(value Resolution) (Resolution, error) {
	for name, field := range map[string]string{"ID": value.ID, "subject": value.Subject, "accepted head": value.AcceptedHead, "resolver": value.Resolver} {
		if strings.TrimSpace(field) == "" || strings.TrimSpace(field) != field || len(field) > 256 {
			return Resolution{}, fmt.Errorf("fork resolution needs a trimmed %s of at most 256 bytes", name)
		}
	}
	if value.Family != FamilyCheckpoint && value.Family != FamilyRevision && value.Family != FamilySupersession {
		return Resolution{}, fmt.Errorf("unsupported fork family %q", value.Family)
	}
	if value.Family == FamilySupersession {
		if _, _, err := ParseSupersessionSubject(value.Subject); err != nil {
			return Resolution{}, err
		}
		if value.Continuation != "" {
			return Resolution{}, errors.New("a supersession fork resolution has no continuation")
		}
	}
	if value.Family == FamilyRevision {
		if _, _, _, err := ParseRevisionSubject(value.Subject); err != nil {
			return Resolution{}, err
		}
		if value.Continuation != "" {
			if _, _, _, err := ParseRevisionSubject(value.Continuation); err != nil {
				return Resolution{}, err
			}
		}
	}
	if value.Authority != "user_reported" && value.Authority != "caller_asserted" {
		return Resolution{}, fmt.Errorf("fork resolution authority must be user_reported or caller_asserted")
	}
	validSchema := value.SchemaVersion == SchemaVersion
	if value.Family == FamilyRevision {
		validSchema = value.SchemaVersion == SchemaVersion || value.SchemaVersion == RevisionSchemaVersion
	}
	if !validSchema || value.RecordedAt.IsZero() {
		return Resolution{}, errors.New("fork resolution needs a supported schema and a time")
	}
	if len(value.RejectedHeads) == 0 || len(value.RejectedHeads) > 64 {
		return Resolution{}, errors.New("fork resolution rejects between 1 and 64 heads")
	}
	rejected := append([]string{}, value.RejectedHeads...)
	sort.Strings(rejected)
	for index, id := range rejected {
		if strings.TrimSpace(id) != id || id == "" || id == value.AcceptedHead || (index > 0 && rejected[index-1] == id) {
			return Resolution{}, fmt.Errorf("fork resolution has an invalid rejected head %q", id)
		}
	}
	if value.Continuation != strings.TrimSpace(value.Continuation) {
		return Resolution{}, errors.New("fork resolution continuation must be trimmed")
	}
	value.RejectedHeads, value.RecordedAt = rejected, value.RecordedAt.UTC()
	return value, nil
}

// Insert appends a validated resolution.
func Insert(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, value Resolution) error {
	value, err := Validate(value)
	if err != nil {
		return err
	}
	rejected, err := json.Marshal(value.RejectedHeads)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO fork_resolutions(project_id,id,family,subject_id,accepted_head,rejected_heads_json,continuation_id,resolver,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,NULLIF(?,''),?,?,?,?)`,
		value.ProjectID, value.ID, value.Family, value.Subject, value.AcceptedHead, string(rejected), value.Continuation, value.Resolver, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion)
	return err
}

// All returns every resolution of a project in recorded order.
func All(ctx context.Context, db queryer, projectID string) ([]Resolution, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,family,subject_id,accepted_head,rejected_heads_json,COALESCE(continuation_id,''),resolver,authority,recorded_at,schema_version FROM fork_resolutions WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Resolution
	for rows.Next() {
		value := Resolution{ProjectID: projectID}
		var rejected, recorded string
		if err := rows.Scan(&value.ID, &value.Family, &value.Subject, &value.AcceptedHead, &rejected, &value.Continuation, &value.Resolver, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rejected), &value.RejectedHeads); err != nil {
			return nil, err
		}
		if value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func rejectedHeads(ctx context.Context, db queryer, projectID, family string) (map[string]bool, error) {
	values, err := All(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	rejected := map[string]bool{}
	for _, value := range values {
		if value.Family == family {
			for _, id := range value.RejectedHeads {
				rejected[id] = true
			}
		}
	}
	return rejected, nil
}

func rejectedRevisionHeads(ctx context.Context, db queryer, projectID string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT f.rejected_heads_json,f.schema_version FROM fork_resolutions f WHERE f.project_id=? AND f.family='revision' ORDER BY f.recorded_at,f.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rejected := map[string]bool{}
	for rows.Next() {
		var body string
		var schema int
		if err := rows.Scan(&body, &schema); err != nil {
			return nil, err
		}
		var heads []string
		if err := json.Unmarshal([]byte(body), &heads); err != nil {
			return nil, err
		}
		for _, head := range heads {
			if schema == RevisionSchemaVersion {
				rejected[head] = true
				continue
			}
			var content string
			err := db.QueryRowContext(ctx, `SELECT content_sha256 FROM fork_head_observations WHERE project_id=? AND envelope_sha256=?`, projectID, head).Scan(&content)
			if err == nil {
				rejected[content] = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
	}
	return rejected, rows.Err()
}
