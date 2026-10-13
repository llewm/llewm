package checkpoint

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// BranchesSchemaVersion is the checkpoint_branches row and portable payload version.
const BranchesSchemaVersion = 1

// MaxBranchBytes bounds one recorded branch name.
const MaxBranchBytes = 255

// CodeBranch is the branch one repository had checked out when a checkpoint
// was written. An empty branch means a detached HEAD.
type CodeBranch struct {
	Member string `json:"member,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// Branches is which branch each repository was on when a checkpoint was
// written, as the writing machine observed it. It is a selection hint, never
// a key: branches are renamed, squashed, and deleted while memory outlives them.
type Branches struct {
	ProjectID     string       `json:"project_id,omitempty"`
	CheckpointID  string       `json:"checkpoint_id"`
	Branches      []CodeBranch `json:"branches"`
	Authority     string       `json:"authority"`
	ObservedAt    time.Time    `json:"observed_at"`
	SchemaVersion int          `json:"schema_version"`
}

// ValidBranchName reports whether a short branch name is safe to record: Git
// already refuses spaces and control characters in ref names.
func ValidBranchName(name string) bool {
	if name == "" || len(name) > MaxBranchBytes || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '\\' {
			return false
		}
	}
	return true
}

// ValidateBranches checks branches and returns them in canonical (member) order.
func ValidateBranches(value Branches) (Branches, error) {
	if strings.TrimSpace(value.CheckpointID) == "" || value.CheckpointID != strings.TrimSpace(value.CheckpointID) {
		return Branches{}, errors.New("checkpoint branches need a checkpoint ID")
	}
	if value.Authority != "machine_observed" || value.SchemaVersion != BranchesSchemaVersion || value.ObservedAt.IsZero() {
		return Branches{}, errors.New("checkpoint branches must be machine_observed, schema 1, with a time")
	}
	if len(value.Branches) == 0 || len(value.Branches) > MaxCodeHeads {
		return Branches{}, fmt.Errorf("checkpoint branches must name between 1 and %d repositories", MaxCodeHeads)
	}
	branches := append([]CodeBranch{}, value.Branches...)
	sort.Slice(branches, func(i, j int) bool { return branches[i].Member < branches[j].Member })
	for index, branch := range branches {
		if branch.Member != strings.Trim(strings.TrimSpace(branch.Member), "/") || strings.Contains(branch.Member, "..") || strings.Contains(branch.Member, "\\") || (branch.Branch != "" && !ValidBranchName(branch.Branch)) {
			return Branches{}, fmt.Errorf("checkpoint branches have an invalid entry %q@%q", branch.Member, branch.Branch)
		}
		if index > 0 && branches[index-1].Member == branch.Member {
			return Branches{}, fmt.Errorf("checkpoint branches name member %q twice", branch.Member)
		}
	}
	value.Branches = branches
	value.ObservedAt = value.ObservedAt.UTC()
	return value, nil
}

// InsertBranches appends validated checkpoint branches.
func InsertBranches(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, value Branches) error {
	value, err := ValidateBranches(value)
	if err != nil {
		return err
	}
	body, err := json.Marshal(value.Branches)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO checkpoint_branches(project_id,checkpoint_id,branches_json,authority,observed_at,schema_version) VALUES(?,?,?,?,?,?)`,
		value.ProjectID, value.CheckpointID, string(body), value.Authority, value.ObservedAt.Format(time.RFC3339Nano), value.SchemaVersion)
	return err
}

const branchesColumns = `checkpoint_id,branches_json,authority,observed_at,schema_version`

func scanBranches(row scanner, projectID string) (Branches, error) {
	var value Branches
	var body, observed string
	if err := row.Scan(&value.CheckpointID, &body, &value.Authority, &observed, &value.SchemaVersion); err != nil {
		return Branches{}, err
	}
	value.ProjectID = projectID
	if err := json.Unmarshal([]byte(body), &value.Branches); err != nil {
		return Branches{}, err
	}
	var err error
	value.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
	return value, err
}

// GetBranches returns a checkpoint's branches, or nil when none were recorded
// (checkpoints written before branches were recorded, or not yet synced).
func GetBranches(ctx context.Context, db rowQueryer, projectID, checkpointID string) (*Branches, error) {
	value, err := scanBranches(db.QueryRowContext(ctx, `SELECT `+branchesColumns+` FROM checkpoint_branches WHERE project_id=? AND checkpoint_id=?`, projectID, checkpointID), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// AllBranches returns every checkpoint's branches in observed order.
func AllBranches(ctx context.Context, db queryer, projectID string) ([]Branches, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+branchesColumns+` FROM checkpoint_branches WHERE project_id=? ORDER BY observed_at,checkpoint_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Branches
	for rows.Next() {
		value, err := scanBranches(rows, projectID)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
