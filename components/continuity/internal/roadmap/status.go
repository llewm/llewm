package roadmap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/project"
)

func LifecycleContext(status Status) string {
	if status.Freshness == "watermark_unavailable" {
		return "Continuity roadmap scan (machine-observed): freshness=watermark_unavailable. " + status.Disclaimer
	}
	if status.Freshness != "changed" && !status.ReviewDue {
		return ""
	}
	reasons := "none"
	if len(status.DueReasons) > 0 {
		reasons = strings.Join(status.DueReasons, ",")
	}
	message := fmt.Sprintf(
		"Continuity roadmap scan (machine-observed): freshness=%s; review_due=%t; due_reasons=%s; changed_bound_files=%d; affected_nodes=%d. Roadmap meaning was not reevaluated; no model was invoked and no sync was created.",
		status.Freshness,
		status.ReviewDue,
		reasons,
		len(status.ChangedFiles),
		len(status.AffectedNodes),
	)
	if status.ReviewDue {
		message += " A bounded source-backed review packet is available on request."
	}
	return message
}

func (service Service) latestSync(ctx context.Context, projectID string) (*SyncRun, error) {
	var run SyncRun
	var client, model, invocation sql.NullString
	var nodesJSON, filesJSON, completed string
	err := service.DB.QueryRowContext(ctx, `SELECT id, git_commit, client, model, invocation_id, reviewed_nodes_json, reviewed_files_json, authority, git_authority, completed_at, schema_version FROM roadmap_sync_runs WHERE project_id = ? ORDER BY completed_at DESC, id DESC LIMIT 1`, projectID).Scan(&run.ID, &run.GitCommit, &client, &model, &invocation, &nodesJSON, &filesJSON, &run.Authority, &run.GitAuthority, &completed, &run.SchemaVersion)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run.ProjectID, run.Client, run.Model, run.InvocationID = projectID, nullable(client), nullable(model), nullable(invocation)
	if err := json.Unmarshal([]byte(nodesJSON), &run.ReviewedNodes); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(filesJSON), &run.ReviewedFiles); err != nil {
		return nil, err
	}
	if run.CompletedAt, err = time.Parse(time.RFC3339Nano, completed); err != nil {
		return nil, err
	}
	return &run, nil
}

func nullable(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func (service Service) latestAcceptedRoutine(ctx context.Context, projectID string) (*ReviewRoutine, error) {
	var routine ReviewRoutine
	var commits, elapsed, files sql.NullInt64
	var sourceRef sql.NullString
	var recorded string
	err := service.DB.QueryRowContext(ctx, `SELECT r.routine_id, r.revision, r.title, r.status, r.commit_threshold, r.elapsed_seconds_threshold, r.changed_files_threshold, r.source, r.source_ref, r.authority, r.recorded_at, r.schema_version
		FROM roadmap_review_routines r
		WHERE r.project_id = ? AND r.status = 'accepted'
		AND r.revision = (SELECT MAX(latest.revision) FROM roadmap_review_routines latest WHERE latest.project_id = r.project_id AND latest.routine_id = r.routine_id)
		ORDER BY r.recorded_at DESC, r.routine_id DESC LIMIT 1`, projectID).Scan(&routine.ID, &routine.Revision, &routine.Title, &routine.Status, &commits, &elapsed, &files, &routine.Source, &sourceRef, &routine.Authority, &recorded, &routine.SchemaVersion)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	routine.ProjectID, routine.SourceRef = projectID, nullable(sourceRef)
	if commits.Valid {
		value := int(commits.Int64)
		routine.CommitThreshold = &value
	}
	if elapsed.Valid {
		value := elapsed.Int64
		routine.ElapsedSecondsThreshold = &value
	}
	if files.Valid {
		value := int(files.Int64)
		routine.ChangedFilesThreshold = &value
	}
	if routine.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
		return nil, err
	}
	return &routine, nil
}

func (service Service) Status(ctx context.Context, projectID string) (Status, error) {
	if _, err := text("project ID", projectID, true); err != nil {
		return Status{}, err
	}
	repository := service.repository()
	head, err := repository.Head(ctx)
	if errors.Is(err, project.ErrWorkspaceRoot) {
		// Freshness is per repository; a workspace folder has none of its own.
		return Status{SchemaVersion: SchemaVersion, ProjectID: projectID, ChangedFiles: make([]FileChange, 0), AffectedNodes: make([]AffectedNode, 0), Freshness: "not_repository", DueReasons: make([]string, 0), Disclaimer: "The project root is a workspace folder, not a Git repository; roadmap freshness is not computed here."}, nil
	}
	if err != nil {
		return Status{}, err
	}
	dirty, err := repository.Dirty(ctx)
	if err != nil {
		return Status{}, err
	}
	last, err := service.latestSync(ctx, projectID)
	if err != nil {
		return Status{}, err
	}
	routine, err := service.latestAcceptedRoutine(ctx, projectID)
	if err != nil {
		return Status{}, err
	}
	result := Status{SchemaVersion: SchemaVersion, ProjectID: projectID, CurrentCommit: head, WorkingTreeDirty: dirty, LastSync: last, ChangedFiles: make([]FileChange, 0), AffectedNodes: make([]AffectedNode, 0), Freshness: "no_sync", ReviewRoutine: routine, DueReasons: make([]string, 0), Disclaimer: "From Continuity saved state and deterministic repository observations; roadmap meaning was not reevaluated now."}
	if last == nil {
		if routine != nil {
			result.ReviewDue, result.DueReasons = true, append(result.DueReasons, "no_model_sync")
		}
		return result, nil
	}
	count, err := repository.CountCommits(ctx, last.GitCommit, head)
	if err != nil {
		// The review was recorded (often on another machine) at a commit this
		// checkout cannot count from: usually memory is ahead of the code.
		result.Freshness = "watermark_unavailable"
		result.Disclaimer = fmt.Sprintf("The last roadmap review was recorded at %s, which this checkout cannot count commits from (%v). Memory is probably ahead of the code: pull first. Freshness was not computed.", last.GitCommit, err)
		return result, nil
	}
	elapsed := int64(service.now().Sub(last.CompletedAt).Seconds())
	if elapsed < 0 {
		elapsed = 0
	}
	result.CommitsSinceSync, result.ElapsedSeconds = &count, &elapsed
	bindings, err := service.currentBindings(ctx, projectID)
	if err != nil {
		return Status{}, err
	}
	current, owners, err := service.capture(ctx, bindings)
	if err != nil {
		return Status{}, err
	}
	previous, err := service.syncFiles(ctx, projectID, last.ID)
	if err != nil {
		return Status{}, err
	}
	allPaths := map[string]bool{}
	for path := range current {
		allPaths[path] = true
	}
	for path := range previous {
		allPaths[path] = true
	}
	nodeChanges := map[string]map[string]bool{}
	nodeMeta := map[string]bindingView{}
	for _, path := range sortedKeys(allPaths) {
		before, beforeOK := previous[path]
		after, afterOK := current[path]
		if beforeOK && afterOK && before == after {
			continue
		}
		state := "modified"
		if !beforeOK || before.state == "missing" {
			state = "added"
		}
		if !afterOK || after.state == "missing" {
			state = "deleted"
		}
		change := FileChange{Path: path, State: state, PreviousHash: before.hash, CurrentHash: after.hash, NodeIDs: make([]string, 0), Relationships: make([]string, 0)}
		pathOwners := owners[path]
		if len(pathOwners) == 0 {
			for _, binding := range bindings {
				if binding.Path == path || (binding.PathKind == "directory" && strings.HasPrefix(path, strings.TrimSuffix(binding.Path, "/")+"/")) {
					pathOwners = append(pathOwners, binding)
				}
			}
		}
		for _, owner := range pathOwners {
			change.NodeIDs = appendUnique(change.NodeIDs, owner.NodeID)
			change.Relationships = appendUnique(change.Relationships, owner.Relationship)
			if nodeChanges[owner.NodeID] == nil {
				nodeChanges[owner.NodeID] = map[string]bool{}
			}
			nodeChanges[owner.NodeID][path] = true
			nodeMeta[owner.NodeID] = owner
		}
		sort.Strings(change.NodeIDs)
		sort.Strings(change.Relationships)
		result.ChangedFiles = append(result.ChangedFiles, change)
	}
	for _, nodeID := range sortedKeys(nodeChanges) {
		meta := nodeMeta[nodeID]
		result.AffectedNodes = append(result.AffectedNodes, AffectedNode{ID: nodeID, Title: meta.NodeTitle, Revision: meta.NodeRevision, ChangedFiles: sortedKeys(nodeChanges[nodeID])})
	}
	result.Freshness = "current"
	if count > 0 || dirty || len(result.ChangedFiles) > 0 {
		result.Freshness = "changed"
	}
	if routine != nil {
		if routine.CommitThreshold != nil && count >= *routine.CommitThreshold {
			result.DueReasons = append(result.DueReasons, "commit_threshold")
		}
		if routine.ElapsedSecondsThreshold != nil && elapsed >= *routine.ElapsedSecondsThreshold {
			result.DueReasons = append(result.DueReasons, "elapsed_time_threshold")
		}
		if routine.ChangedFilesThreshold != nil && len(result.ChangedFiles) >= *routine.ChangedFilesThreshold {
			result.DueReasons = append(result.DueReasons, "changed_files_threshold")
		}
		result.ReviewDue = len(result.DueReasons) > 0
	}
	return result, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (service Service) syncFiles(ctx context.Context, projectID, syncID string) (map[string]fileState, error) {
	rows, err := service.DB.QueryContext(ctx, `SELECT path, content_hash, state FROM roadmap_sync_files WHERE project_id = ? AND sync_id = ? ORDER BY path`, projectID, syncID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]fileState{}
	for rows.Next() {
		var path, state string
		var hash sql.NullString
		if err := rows.Scan(&path, &hash, &state); err != nil {
			return nil, err
		}
		result[path] = fileState{hash: nullable(hash), state: state}
	}
	return result, rows.Err()
}
