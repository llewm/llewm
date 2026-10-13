package checkpoint

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ContextSchemaVersion is the checkpoint_contexts row and portable payload version.
const ContextSchemaVersion = 1

// MaxCodeHeads bounds how many repositories one checkpoint context records.
const MaxCodeHeads = 64

// CodeHead is the HEAD commit of one repository when a checkpoint was written.
// Member is the workspace member path, or empty for a single-repository project.
type CodeHead struct {
	Member string `json:"member,omitempty"`
	Commit string `json:"commit"`
	// Branch is the checked-out branch, or empty for a detached HEAD. It is
	// stored as the checkpoint's branches, not in the context, so context
	// records keep their bytes (see Branches).
	Branch string `json:"-"`
}

// Context is what a checkpoint was written on, as the writing machine observed
// it: the checkpoint of the same task it continued, and each repository's HEAD.
type Context struct {
	ProjectID         string     `json:"project_id,omitempty"`
	CheckpointID      string     `json:"checkpoint_id"`
	BasedOnCheckpoint string     `json:"based_on_checkpoint_id,omitempty"`
	CodeHeads         []CodeHead `json:"code_heads"`
	Authority         string     `json:"authority"`
	ObservedAt        time.Time  `json:"observed_at"`
	SchemaVersion     int        `json:"schema_version"`
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ValidateContext checks a context and returns it with code heads in canonical
// (member) order.
func ValidateContext(value Context) (Context, error) {
	if strings.TrimSpace(value.CheckpointID) == "" || value.CheckpointID != strings.TrimSpace(value.CheckpointID) {
		return Context{}, errors.New("checkpoint context needs a checkpoint ID")
	}
	if value.BasedOnCheckpoint != strings.TrimSpace(value.BasedOnCheckpoint) || value.BasedOnCheckpoint == value.CheckpointID {
		return Context{}, errors.New("checkpoint context has an invalid based-on checkpoint")
	}
	if value.Authority != "machine_observed" || value.SchemaVersion != ContextSchemaVersion || value.ObservedAt.IsZero() {
		return Context{}, errors.New("checkpoint context must be machine_observed, schema 1, with a time")
	}
	if len(value.CodeHeads) > MaxCodeHeads {
		return Context{}, fmt.Errorf("checkpoint context records more than %d repositories", MaxCodeHeads)
	}
	heads := append([]CodeHead{}, value.CodeHeads...)
	sort.Slice(heads, func(i, j int) bool { return heads[i].Member < heads[j].Member })
	for index, head := range heads {
		if !commitPattern.MatchString(head.Commit) || head.Member != strings.Trim(strings.TrimSpace(head.Member), "/") || strings.Contains(head.Member, "..") || strings.Contains(head.Member, "\\") {
			return Context{}, fmt.Errorf("checkpoint context has an invalid code head %q@%q", head.Member, head.Commit)
		}
		if index > 0 && heads[index-1].Member == head.Member {
			return Context{}, fmt.Errorf("checkpoint context names member %q twice", head.Member)
		}
	}
	value.CodeHeads = heads
	value.ObservedAt = value.ObservedAt.UTC()
	return value, nil
}

// InsertContext appends a validated context.
func InsertContext(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, value Context) error {
	value, err := ValidateContext(value)
	if err != nil {
		return err
	}
	heads, err := json.Marshal(value.CodeHeads)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO checkpoint_contexts(project_id,checkpoint_id,based_on_checkpoint_id,code_heads_json,authority,observed_at,schema_version) VALUES(?,?,NULLIF(?,''),?,?,?,?)`,
		value.ProjectID, value.CheckpointID, value.BasedOnCheckpoint, string(heads), value.Authority, value.ObservedAt.Format(time.RFC3339Nano), value.SchemaVersion)
	return err
}

// basedOnCheckpoint is the checkpoint a new one continues: its explicit
// predecessor, else its task's current tip, else none (no task).
func basedOnCheckpoint(ctx context.Context, tx *sql.Tx, value Checkpoint) (string, error) {
	if value.BasedOn != "" {
		var taskID sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT task_id FROM checkpoints WHERE project_id=? AND id=?`, value.ProjectID, value.BasedOn).Scan(&taskID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && taskID.String != value.TaskID) {
			return "", fmt.Errorf("checkpoint %s is not a checkpoint of task %q", value.BasedOn, value.TaskID)
		}
		return value.BasedOn, err
	}
	if value.PredecessorCheckpointID != "" || value.TaskID == "" {
		return value.PredecessorCheckpointID, nil
	}
	return currentTaskTip(ctx, tx, value.ProjectID, value.TaskID)
}

// recordContext writes the context of a checkpoint just created in tx.
func recordContext(ctx context.Context, tx *sql.Tx, value Checkpoint, basedOn string) error {
	heads := value.CodeHeads
	if heads == nil {
		heads = []CodeHead{}
	}
	if err := InsertContext(ctx, tx, Context{ProjectID: value.ProjectID, CheckpointID: value.ID, BasedOnCheckpoint: basedOn, CodeHeads: heads, Authority: "machine_observed", ObservedAt: value.CreatedAt, SchemaVersion: ContextSchemaVersion}); err != nil {
		return err
	}
	if len(heads) == 0 {
		return nil
	}
	branches := make([]CodeBranch, 0, len(heads))
	for _, head := range heads {
		branches = append(branches, CodeBranch{Member: head.Member, Branch: head.Branch})
	}
	return InsertBranches(ctx, tx, Branches{ProjectID: value.ProjectID, CheckpointID: value.ID, Branches: branches, Authority: "machine_observed", ObservedAt: value.CreatedAt, SchemaVersion: BranchesSchemaVersion})
}

const contextColumns = `checkpoint_id,based_on_checkpoint_id,code_heads_json,authority,observed_at,schema_version`

type scanner interface{ Scan(...any) error }

func scanContext(row scanner, projectID string) (Context, error) {
	var value Context
	var basedOn sql.NullString
	var heads, observed string
	if err := row.Scan(&value.CheckpointID, &basedOn, &heads, &value.Authority, &observed, &value.SchemaVersion); err != nil {
		return Context{}, err
	}
	value.ProjectID, value.BasedOnCheckpoint = projectID, basedOn.String
	if err := json.Unmarshal([]byte(heads), &value.CodeHeads); err != nil {
		return Context{}, err
	}
	var err error
	value.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
	return value, err
}

// GetContext returns a checkpoint's context, or nil when none was recorded
// (checkpoints written before contexts existed, or not yet synced).
func GetContext(ctx context.Context, db rowQueryer, projectID, checkpointID string) (*Context, error) {
	value, err := scanContext(db.QueryRowContext(ctx, `SELECT `+contextColumns+` FROM checkpoint_contexts WHERE project_id=? AND checkpoint_id=?`, projectID, checkpointID), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// AllContexts returns every checkpoint context of a project in observed order.
func AllContexts(ctx context.Context, db queryer, projectID string) ([]Context, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+contextColumns+` FROM checkpoint_contexts WHERE project_id=? ORDER BY observed_at,checkpoint_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Context
	for rows.Next() {
		value, err := scanContext(rows, projectID)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// Load returns one stored checkpoint, or nil when it does not exist.
func Load(ctx context.Context, db rowQueryer, projectID, id string) (*Checkpoint, error) {
	var payload string
	err := db.QueryRowContext(ctx, `SELECT payload_json FROM checkpoints WHERE project_id = ? AND id = ?`, projectID, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var value Checkpoint
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return nil, fmt.Errorf("decode checkpoint %s: %w", id, err)
	}
	return &value, nil
}
