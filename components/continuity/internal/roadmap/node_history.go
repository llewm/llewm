package roadmap

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"
)

const (
	DefaultNodeHistoryLimit = 20
	MaxNodeHistoryLimit     = 64
	MaxNodeHistoryRevisions = 1024
)

type NodeHistoryOptions struct {
	NodeID string `json:"node_id"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type NodeHistory struct {
	SchemaVersion int            `json:"schema_version"`
	ProjectID     string         `json:"project_id"`
	NodeID        string         `json:"node_id"`
	Revisions     []NodeRevision `json:"revisions"`
	Offset        int            `json:"offset"`
	Limit         int            `json:"limit"`
	Total         int            `json:"total"`
	Returned      int            `json:"returned"`
	Omitted       int            `json:"omitted"`
	Truncated     bool           `json:"truncated"`
	Authority     string         `json:"authority"`
	Disclaimer    string         `json:"disclaimer"`
}

type NodeRevision struct {
	Revision      int       `json:"revision"`
	Title         string    `json:"title"`
	Status        string    `json:"status"`
	Priority      int       `json:"priority"`
	Kind          string    `json:"kind"`
	TargetDate    string    `json:"target_date,omitempty"`
	TargetRelease string    `json:"target_release,omitempty"`
	TargetHorizon string    `json:"target_horizon,omitempty"`
	EvidenceRefs  []string  `json:"evidence_refs"`
	SourcePath    string    `json:"source_path"`
	SourceRef     string    `json:"source_ref,omitempty"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	Current       bool      `json:"current"`
	ChangedFields []string  `json:"changed_fields"`
}

// NodeHistory returns bounded immutable roadmap node revisions and deterministic
// field differences. It does not read source files or change saved records.
func (service Service) NodeHistory(ctx context.Context, projectID string, options NodeHistoryOptions) (NodeHistory, error) {
	if _, err := text("project ID", projectID, true); err != nil {
		return NodeHistory{}, err
	}
	var err error
	if options.NodeID, err = text("roadmap node ID", options.NodeID, true); err != nil {
		return NodeHistory{}, err
	}
	if options.Limit == 0 {
		options.Limit = DefaultNodeHistoryLimit
	}
	if options.Offset < 0 || options.Limit < 1 || options.Limit > MaxNodeHistoryLimit {
		return NodeHistory{}, fmt.Errorf("node history offset must be nonnegative and limit must be between 1 and %d", MaxNodeHistoryLimit)
	}
	tx, err := service.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NodeHistory{}, err
	}
	defer tx.Rollback()
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM roadmap_nodes WHERE project_id=? AND node_id=?`, projectID, options.NodeID).Scan(&total); err != nil {
		return NodeHistory{}, err
	}
	if total == 0 {
		return NodeHistory{}, fmt.Errorf("roadmap node %s not found", options.NodeID)
	}
	if total > MaxNodeHistoryRevisions {
		return NodeHistory{}, fmt.Errorf("roadmap node history exceeds %d-revision limit", MaxNodeHistoryRevisions)
	}
	rows, err := tx.QueryContext(ctx, `SELECT revision,title,status,priority,kind,COALESCE(target_date,''),COALESCE(target_release,''),COALESCE(target_horizon,''),evidence_refs_json,source_path,source_ref,authority,recorded_at
		FROM roadmap_nodes WHERE project_id=? AND node_id=? ORDER BY revision LIMIT ?`, projectID, options.NodeID, MaxNodeHistoryRevisions+1)
	if err != nil {
		return NodeHistory{}, err
	}
	revisions := make([]NodeRevision, 0, total)
	for rows.Next() {
		var revision NodeRevision
		var sourceRef sql.NullString
		var evidence string
		var recorded string
		if err := rows.Scan(&revision.Revision, &revision.Title, &revision.Status, &revision.Priority, &revision.Kind, &revision.TargetDate, &revision.TargetRelease, &revision.TargetHorizon, &evidence, &revision.SourcePath, &sourceRef, &revision.Authority, &recorded); err != nil {
			rows.Close()
			return NodeHistory{}, err
		}
		if err := json.Unmarshal([]byte(evidence), &revision.EvidenceRefs); err != nil {
			rows.Close()
			return NodeHistory{}, err
		}
		revision.SourceRef = nullable(sourceRef)
		if revision.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			rows.Close()
			return NodeHistory{}, err
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return NodeHistory{}, err
	}
	if err := rows.Close(); err != nil {
		return NodeHistory{}, err
	}
	if err := tx.Commit(); err != nil {
		return NodeHistory{}, err
	}
	for i := range revisions {
		if i == len(revisions)-1 {
			revisions[i].Current = true
		}
		if i == 0 {
			revisions[i].ChangedFields = []string{"initial_revision"}
			continue
		}
		revisions[i].ChangedFields = changedNodeFields(revisions[i-1], revisions[i])
	}
	start := options.Offset
	if start > total {
		start = total
	}
	end := start + options.Limit
	if end > total {
		end = total
	}
	page := revisions[start:end]
	return NodeHistory{
		SchemaVersion: SchemaVersion, ProjectID: projectID, NodeID: options.NodeID, Revisions: page,
		Offset: options.Offset, Limit: options.Limit, Total: total, Returned: len(page), Omitted: total - len(page), Truncated: total > len(page),
		Authority:  "machine_observed",
		Disclaimer: "Revisions and changed fields are derived from append-only saved roadmap node rows. Bindings, relationships, and source-file content are not included; a changed field does not imply an error or semantic change in project intent.",
	}, nil
}

func changedNodeFields(previous, current NodeRevision) []string {
	fields := make([]string, 0, 5)
	if previous.Title != current.Title {
		fields = append(fields, "title")
	}
	if previous.Status != current.Status {
		fields = append(fields, "status")
	}
	if previous.Priority != current.Priority {
		fields = append(fields, "priority")
	}
	if previous.Kind != current.Kind {
		fields = append(fields, "kind")
	}
	if previous.TargetDate != current.TargetDate {
		fields = append(fields, "target_date")
	}
	if previous.TargetRelease != current.TargetRelease {
		fields = append(fields, "target_release")
	}
	if previous.TargetHorizon != current.TargetHorizon {
		fields = append(fields, "target_horizon")
	}
	if !slices.Equal(previous.EvidenceRefs, current.EvidenceRefs) {
		fields = append(fields, "evidence_refs")
	}
	if previous.SourcePath != current.SourcePath {
		fields = append(fields, "source_path")
	}
	if previous.SourceRef != current.SourceRef {
		fields = append(fields, "source_ref")
	}
	sort.Strings(fields)
	return fields
}
