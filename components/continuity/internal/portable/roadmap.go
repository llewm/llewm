package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
)

const roadmapTextMaxBytes = 16 << 10

type roadmapNodePayload struct {
	Title                    string   `json:"title"`
	Status                   string   `json:"status"`
	Priority                 int      `json:"priority"`
	Kind                     string   `json:"kind,omitempty"`
	TargetDate               string   `json:"target_date,omitempty"`
	TargetRelease            string   `json:"target_release,omitempty"`
	TargetHorizon            string   `json:"target_horizon,omitempty"`
	EvidenceRefs             []string `json:"evidence_refs,omitempty"`
	SourcePath               string   `json:"source_path"`
	SourceRef                string   `json:"source_ref,omitempty"`
	OperationalSchemaVersion int      `json:"operational_schema_version"`
}

type roadmapBindingPayload struct {
	NodeID                   string `json:"node_id"`
	NodeRevision             int    `json:"node_revision"`
	Path                     string `json:"path"`
	PathKind                 string `json:"path_kind"`
	Relationship             string `json:"relationship"`
	OperationalSchemaVersion int    `json:"operational_schema_version"`
}

type roadmapLinkPayload struct {
	NodeID                   string `json:"node_id"`
	NodeRevision             int    `json:"node_revision"`
	TargetNodeID             string `json:"target_node_id"`
	Relationship             string `json:"relationship"`
	OperationalSchemaVersion int    `json:"operational_schema_version"`
}

type roadmapRoutinePayload struct {
	Title                    string `json:"title"`
	Status                   string `json:"status"`
	CommitThreshold          *int   `json:"commit_threshold,omitempty"`
	ElapsedSecondsThreshold  *int64 `json:"elapsed_seconds_threshold,omitempty"`
	ChangedFilesThreshold    *int   `json:"changed_files_threshold,omitempty"`
	Source                   string `json:"source"`
	SourceRef                string `json:"source_ref,omitempty"`
	OperationalSchemaVersion int    `json:"operational_schema_version"`
}

type roadmapSyncPayload struct {
	GitCommit                string   `json:"git_commit"`
	Client                   string   `json:"client,omitempty"`
	Model                    string   `json:"model"`
	InvocationID             string   `json:"invocation_id,omitempty"`
	ReviewedNodes            []string `json:"reviewed_nodes"`
	ReviewedFiles            []string `json:"reviewed_files"`
	GitAuthority             string   `json:"git_authority"`
	SourceSnapshotOmitted    bool     `json:"source_snapshot_omitted"`
	OperationalSchemaVersion int      `json:"operational_schema_version"`
}

type portableRoadmapRecords struct {
	Nodes    map[string]roadmap.Node
	Bindings map[string]roadmap.Binding
	Links    map[string]roadmap.NodeLink
	Routines map[string]roadmap.ReviewRoutine
	Syncs    map[string]roadmap.SyncRun
}

func newPortableRoadmapRecords() portableRoadmapRecords {
	return portableRoadmapRecords{
		Nodes: make(map[string]roadmap.Node), Bindings: make(map[string]roadmap.Binding),
		Links: make(map[string]roadmap.NodeLink), Routines: make(map[string]roadmap.ReviewRoutine),
		Syncs: make(map[string]roadmap.SyncRun),
	}
}

func loadRoadmapSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, map[string]struct{}, error) {
	result := make([]EnvelopeSpec, 0)
	invocations := make(map[string]struct{})
	nodeRows, err := exportRows(ctx, source, "roadmap_nodes", `SELECT node_id,revision,title,status,priority,kind,COALESCE(target_date,''),COALESCE(target_release,''),COALESCE(target_horizon,''),evidence_refs_json,source_path,COALESCE(source_ref,''),authority,recorded_at,schema_version FROM roadmap_nodes WHERE project_id=? ORDER BY node_id,revision`, projectID)
	if err != nil {
		return nil, nil, err
	}
	for nodeRows.Next() {
		var value roadmap.Node
		var recorded string
		var evidence string
		if err := nodeRows.Scan(&value.ID, &value.Revision, &value.Title, &value.Status, &value.Priority, &value.Kind, &value.TargetDate, &value.TargetRelease, &value.TargetHorizon, &evidence, &value.SourcePath, &value.SourceRef, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			nodeRows.Close()
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(evidence), &value.EvidenceRefs); err != nil {
			nodeRows.Close()
			return nil, nil, err
		}
		value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			nodeRows.Close()
			return nil, nil, err
		}
		value.ProjectID = projectID
		spec, err := roadmapNodeSpec(lineageID, projectID, projectRoot, value)
		if err != nil {
			nodeRows.Close()
			return nil, nil, err
		}
		result = append(result, spec)
	}
	if err := nodeRows.Err(); err != nil {
		nodeRows.Close()
		return nil, nil, err
	}
	if err := nodeRows.Close(); err != nil {
		return nil, nil, err
	}

	bindingRows, err := exportRows(ctx, source, "roadmap_file_bindings", `SELECT id,node_id,node_revision,path,path_kind,relationship,authority,recorded_at,schema_version FROM roadmap_file_bindings WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, nil, err
	}
	for bindingRows.Next() {
		var value roadmap.Binding
		var recorded string
		if err := bindingRows.Scan(&value.ID, &value.NodeID, &value.NodeRevision, &value.Path, &value.PathKind, &value.Relationship, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			bindingRows.Close()
			return nil, nil, err
		}
		value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			bindingRows.Close()
			return nil, nil, err
		}
		value.ProjectID = projectID
		spec, err := roadmapBindingSpec(lineageID, projectID, projectRoot, value)
		if err != nil {
			bindingRows.Close()
			return nil, nil, err
		}
		result = append(result, spec)
	}
	if err := bindingRows.Err(); err != nil {
		bindingRows.Close()
		return nil, nil, err
	}
	if err := bindingRows.Close(); err != nil {
		return nil, nil, err
	}

	linkRows, err := exportRows(ctx, source, "roadmap_node_links", `SELECT id,node_id,node_revision,target_node_id,relationship,authority,recorded_at,schema_version FROM roadmap_node_links WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, nil, err
	}
	for linkRows.Next() {
		var value roadmap.NodeLink
		var recorded string
		if err := linkRows.Scan(&value.ID, &value.NodeID, &value.NodeRevision, &value.TargetNodeID, &value.Relationship, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			linkRows.Close()
			return nil, nil, err
		}
		value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			linkRows.Close()
			return nil, nil, err
		}
		value.ProjectID = projectID
		spec, err := roadmapLinkSpec(lineageID, projectID, projectRoot, value)
		if err != nil {
			linkRows.Close()
			return nil, nil, err
		}
		result = append(result, spec)
	}
	if err := linkRows.Err(); err != nil {
		linkRows.Close()
		return nil, nil, err
	}
	if err := linkRows.Close(); err != nil {
		return nil, nil, err
	}

	routineRows, err := exportRows(ctx, source, "roadmap_review_routines", `SELECT routine_id,revision,title,status,commit_threshold,elapsed_seconds_threshold,changed_files_threshold,source,COALESCE(source_ref,''),authority,recorded_at,schema_version FROM roadmap_review_routines WHERE project_id=? ORDER BY routine_id,revision`, projectID)
	if err != nil {
		return nil, nil, err
	}
	for routineRows.Next() {
		var value roadmap.ReviewRoutine
		var commits, elapsed, files sql.NullInt64
		var recorded string
		if err := routineRows.Scan(&value.ID, &value.Revision, &value.Title, &value.Status, &commits, &elapsed, &files, &value.Source, &value.SourceRef, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			routineRows.Close()
			return nil, nil, err
		}
		if commits.Valid {
			threshold := int(commits.Int64)
			value.CommitThreshold = &threshold
		}
		if elapsed.Valid {
			threshold := elapsed.Int64
			value.ElapsedSecondsThreshold = &threshold
		}
		if files.Valid {
			threshold := int(files.Int64)
			value.ChangedFilesThreshold = &threshold
		}
		value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			routineRows.Close()
			return nil, nil, err
		}
		value.ProjectID = projectID
		spec, err := roadmapRoutineSpec(lineageID, projectID, projectRoot, value)
		if err != nil {
			routineRows.Close()
			return nil, nil, err
		}
		result = append(result, spec)
	}
	if err := routineRows.Err(); err != nil {
		routineRows.Close()
		return nil, nil, err
	}
	if err := routineRows.Close(); err != nil {
		return nil, nil, err
	}

	syncRows, err := exportRows(ctx, source, "roadmap_sync_runs", `SELECT id,git_commit,COALESCE(client,''),COALESCE(model,''),COALESCE(invocation_id,''),reviewed_nodes_json,reviewed_files_json,authority,git_authority,completed_at,schema_version FROM roadmap_sync_runs WHERE project_id=? ORDER BY completed_at,id`, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer syncRows.Close()
	for syncRows.Next() {
		var value roadmap.SyncRun
		var nodesJSON, filesJSON, completed string
		if err := syncRows.Scan(&value.ID, &value.GitCommit, &value.Client, &value.Model, &value.InvocationID, &nodesJSON, &filesJSON, &value.Authority, &value.GitAuthority, &completed, &value.SchemaVersion); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(nodesJSON), &value.ReviewedNodes); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(filesJSON), &value.ReviewedFiles); err != nil {
			return nil, nil, err
		}
		value.CompletedAt, err = time.Parse(time.RFC3339Nano, completed)
		if err != nil {
			return nil, nil, err
		}
		value.ProjectID = projectID
		spec, err := roadmapSyncSpec(lineageID, projectID, projectRoot, value)
		if err != nil {
			return nil, nil, err
		}
		if value.InvocationID != "" {
			invocations[value.InvocationID] = struct{}{}
		}
		result = append(result, spec)
	}
	return result, invocations, syncRows.Err()
}

func roadmapNodeSpec(lineageID, projectID, projectRoot string, value roadmap.Node) (EnvelopeSpec, error) {
	if !portableRoadmapBase(value.ID, value.Authority, value.SchemaVersion, projectID, projectRoot) || value.Revision < 1 {
		return EnvelopeSpec{}, fmt.Errorf("roadmap node %s/%d has invalid identity or provenance", value.ID, value.Revision)
	}
	recordSchema := 2
	kind := value.Kind
	if value.Kind == "work_item" && value.TargetDate == "" && value.TargetRelease == "" && value.TargetHorizon == "" && len(value.EvidenceRefs) == 0 {
		recordSchema, kind = 1, ""
	}
	payload := roadmapNodePayload{Title: portableRoadmapText(value.Title, projectID, projectRoot), Status: value.Status, Priority: value.Priority, Kind: kind, TargetDate: value.TargetDate, TargetRelease: portableRoadmapText(value.TargetRelease, projectID, projectRoot), TargetHorizon: portableRoadmapText(value.TargetHorizon, projectID, projectRoot), EvidenceRefs: portableRoadmapStrings(value.EvidenceRefs, projectID, projectRoot), SourcePath: portableRoadmapText(value.SourcePath, projectID, projectRoot), SourceRef: portableRoadmapText(value.SourceRef, projectID, projectRoot), OperationalSchemaVersion: value.SchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	index, err := json.Marshal(envelopeIndex{Title: payload.Title, Status: payload.Status})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	spec := EnvelopeSpec{LineageID: lineageID, RecordType: "roadmap_node", RecordSchemaVersion: recordSchema, RecordID: value.ID, RecordRevision: value.Revision, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: roadmapNodeRefs(payload), Payload: body}
	return spec, validateRoadmapSpec(spec, payload)
}

func roadmapBindingSpec(lineageID, projectID, projectRoot string, value roadmap.Binding) (EnvelopeSpec, error) {
	if !portableRoadmapBase(value.ID, value.Authority, value.SchemaVersion, projectID, projectRoot) || !portableRoadmapIdentifier(value.NodeID, projectID, projectRoot) || value.NodeRevision < 1 {
		return EnvelopeSpec{}, fmt.Errorf("roadmap binding %s has invalid identity or provenance", value.ID)
	}
	payload := roadmapBindingPayload{NodeID: value.NodeID, NodeRevision: value.NodeRevision, Path: portableRoadmapText(value.Path, projectID, projectRoot), PathKind: value.PathKind, Relationship: value.Relationship, OperationalSchemaVersion: value.SchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	index, err := json.Marshal(envelopeIndex{Title: payload.Path, Status: payload.Relationship})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	spec := EnvelopeSpec{LineageID: lineageID, RecordType: "roadmap_binding", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: roadmapBindingRefs(payload), Payload: body}
	return spec, validateRoadmapSpec(spec, payload)
}

func roadmapLinkSpec(lineageID, projectID, projectRoot string, value roadmap.NodeLink) (EnvelopeSpec, error) {
	if !portableRoadmapBase(value.ID, value.Authority, value.SchemaVersion, projectID, projectRoot) || !portableRoadmapIdentifier(value.NodeID, projectID, projectRoot) || !portableRoadmapIdentifier(value.TargetNodeID, projectID, projectRoot) || value.NodeRevision < 1 {
		return EnvelopeSpec{}, fmt.Errorf("roadmap link %s has invalid identity or provenance", value.ID)
	}
	payload := roadmapLinkPayload{NodeID: value.NodeID, NodeRevision: value.NodeRevision, TargetNodeID: value.TargetNodeID, Relationship: value.Relationship, OperationalSchemaVersion: value.SchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	index, err := json.Marshal(envelopeIndex{Title: payload.TargetNodeID, Status: payload.Relationship})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	spec := EnvelopeSpec{LineageID: lineageID, RecordType: "roadmap_link", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: roadmapLinkRefs(payload), Payload: body}
	return spec, validateRoadmapSpec(spec, payload)
}

func roadmapRoutineSpec(lineageID, projectID, projectRoot string, value roadmap.ReviewRoutine) (EnvelopeSpec, error) {
	if !portableRoadmapBase(value.ID, value.Authority, value.SchemaVersion, projectID, projectRoot) || value.Revision < 1 {
		return EnvelopeSpec{}, fmt.Errorf("roadmap routine %s/%d has invalid identity or provenance", value.ID, value.Revision)
	}
	payload := roadmapRoutinePayload{Title: portableRoadmapText(value.Title, projectID, projectRoot), Status: value.Status, CommitThreshold: value.CommitThreshold, ElapsedSecondsThreshold: value.ElapsedSecondsThreshold, ChangedFilesThreshold: value.ChangedFilesThreshold, Source: portableRoadmapText(value.Source, projectID, projectRoot), SourceRef: portableRoadmapText(value.SourceRef, projectID, projectRoot), OperationalSchemaVersion: value.SchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	index, err := json.Marshal(envelopeIndex{Title: payload.Title, Status: payload.Status})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	spec := EnvelopeSpec{LineageID: lineageID, RecordType: "roadmap_review_routine", RecordSchemaVersion: 1, RecordID: value.ID, RecordRevision: value.Revision, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: roadmapRoutineRefs(payload), Payload: body}
	return spec, validateRoadmapSpec(spec, payload)
}

func roadmapSyncSpec(lineageID, projectID, projectRoot string, value roadmap.SyncRun) (EnvelopeSpec, error) {
	if !portableRoadmapBase(value.ID, value.Authority, value.SchemaVersion, projectID, projectRoot) || value.GitAuthority != "machine_observed" {
		return EnvelopeSpec{}, fmt.Errorf("roadmap sync %s has invalid identity or provenance", value.ID)
	}
	if value.InvocationID != "" && !portableRoadmapIdentifier(value.InvocationID, projectID, projectRoot) {
		return EnvelopeSpec{}, fmt.Errorf("roadmap sync %s has a non-portable invocation identifier", value.ID)
	}
	for _, nodeID := range value.ReviewedNodes {
		if !portableRoadmapIdentifier(nodeID, projectID, projectRoot) {
			return EnvelopeSpec{}, fmt.Errorf("roadmap sync %s has a non-portable reviewed node", value.ID)
		}
	}
	payload := roadmapSyncPayload{GitCommit: value.GitCommit, Client: portableRoadmapText(value.Client, projectID, projectRoot), Model: portableRoadmapText(value.Model, projectID, projectRoot), InvocationID: value.InvocationID, ReviewedNodes: portableRoadmapStrings(value.ReviewedNodes, projectID, projectRoot), ReviewedFiles: portableRoadmapStrings(value.ReviewedFiles, projectID, projectRoot), GitAuthority: value.GitAuthority, SourceSnapshotOmitted: true, OperationalSchemaVersion: value.SchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	index, err := json.Marshal(envelopeIndex{Title: payload.Model, Status: "accepted", SourceCommit: payload.GitCommit})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	spec := EnvelopeSpec{LineageID: lineageID, RecordType: "roadmap_sync", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.CompletedAt, Authority: value.Authority, Index: index, Refs: roadmapSyncRefs(payload), Payload: body}
	return spec, validateRoadmapSpec(spec, payload)
}

func validateRoadmapSpec(spec EnvelopeSpec, payload any) error {
	encoded, err := EncodeEnvelope(spec)
	if err != nil {
		return err
	}
	switch value := payload.(type) {
	case roadmapNodePayload:
		_, err = decodeRoadmapNode(encoded.Envelope, value)
	case roadmapBindingPayload:
		_, err = decodeRoadmapBinding(encoded.Envelope, value)
	case roadmapLinkPayload:
		_, err = decodeRoadmapLink(encoded.Envelope, value)
	case roadmapRoutinePayload:
		_, err = decodeRoadmapRoutine(encoded.Envelope, value)
	case roadmapSyncPayload:
		_, err = decodeRoadmapSync(encoded.Envelope, value)
	default:
		err = fmt.Errorf("unsupported roadmap payload validation type %T", payload)
	}
	return err
}

func decodeRoadmapNode(envelope Envelope, payload roadmapNodePayload) (roadmap.Node, error) {
	if envelope.RecordSchemaVersion == 1 && payload.Kind == "" {
		payload.Kind = "work_item"
		payload.EvidenceRefs = []string{}
	}
	targetDateValid := payload.TargetDate == ""
	if parsed, parseErr := time.Parse(time.DateOnly, payload.TargetDate); parseErr == nil && parsed.Format(time.DateOnly) == payload.TargetDate {
		targetDateValid = true
	}
	if envelope.RecordRevision < 1 || envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || payload.OperationalSchemaVersion != roadmap.SchemaVersion || !validRoadmapText(payload.Title, true) || !oneOfValue(payload.Status, "proposed", "accepted", "active", "blocked", "completed", "deferred", "superseded") || payload.Priority < 0 || payload.Priority > 100 || !oneOfValue(payload.Kind, "initiative", "milestone", "work_item", "research") || !targetDateValid || !validRoadmapText(payload.TargetRelease, false) || !validRoadmapText(payload.TargetHorizon, false) || payload.Kind != "milestone" && (payload.TargetDate != "" || payload.TargetRelease != "" || payload.TargetHorizon != "") || len(payload.EvidenceRefs) > 64 || !validRoadmapStrings(payload.EvidenceRefs) || !validRoadmapPath(payload.SourcePath) || !validRoadmapText(payload.SourceRef, false) {
		return roadmap.Node{}, fmt.Errorf("roadmap node %s/%d is invalid", envelope.RecordID, envelope.RecordRevision)
	}
	if !sameReferences(envelope.Refs, roadmapNodeRefs(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Title, Status: payload.Status}) {
		return roadmap.Node{}, fmt.Errorf("roadmap node %s/%d metadata does not match payload", envelope.RecordID, envelope.RecordRevision)
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	return roadmap.Node{ID: envelope.RecordID, Revision: envelope.RecordRevision, Title: payload.Title, Status: payload.Status, Priority: payload.Priority, Kind: payload.Kind, TargetDate: payload.TargetDate, TargetRelease: payload.TargetRelease, TargetHorizon: payload.TargetHorizon, EvidenceRefs: append([]string{}, payload.EvidenceRefs...), SourcePath: payload.SourcePath, SourceRef: payload.SourceRef, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}, err
}

func decodeRoadmapBinding(envelope Envelope, payload roadmapBindingPayload) (roadmap.Binding, error) {
	if envelope.RecordRevision != 0 || envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || !portableIncomingIdentifier(payload.NodeID) || payload.NodeRevision < 1 || !validRoadmapPath(payload.Path) || !oneOfValue(payload.PathKind, "file", "directory") || !oneOfValue(payload.Relationship, "implementation", "contract", "test", "documentation", "verification") || payload.OperationalSchemaVersion != roadmap.SchemaVersion {
		return roadmap.Binding{}, fmt.Errorf("roadmap binding %s is invalid", envelope.RecordID)
	}
	if !sameReferences(envelope.Refs, roadmapBindingRefs(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Path, Status: payload.Relationship}) {
		return roadmap.Binding{}, fmt.Errorf("roadmap binding %s metadata does not match payload", envelope.RecordID)
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	return roadmap.Binding{ID: envelope.RecordID, NodeID: payload.NodeID, NodeRevision: payload.NodeRevision, Path: payload.Path, PathKind: payload.PathKind, Relationship: payload.Relationship, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}, err
}

func decodeRoadmapLink(envelope Envelope, payload roadmapLinkPayload) (roadmap.NodeLink, error) {
	if envelope.RecordRevision != 0 || envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || !portableIncomingIdentifier(payload.NodeID) || !portableIncomingIdentifier(payload.TargetNodeID) || payload.NodeID == payload.TargetNodeID || payload.NodeRevision < 1 || !oneOfValue(payload.Relationship, "depends_on", "blocks", "parallel_with", "parent_of", "explores") || payload.OperationalSchemaVersion != roadmap.SchemaVersion {
		return roadmap.NodeLink{}, fmt.Errorf("roadmap link %s is invalid", envelope.RecordID)
	}
	if !sameReferences(envelope.Refs, roadmapLinkRefs(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.TargetNodeID, Status: payload.Relationship}) {
		return roadmap.NodeLink{}, fmt.Errorf("roadmap link %s metadata does not match payload", envelope.RecordID)
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	return roadmap.NodeLink{ID: envelope.RecordID, NodeID: payload.NodeID, NodeRevision: payload.NodeRevision, TargetNodeID: payload.TargetNodeID, Relationship: payload.Relationship, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}, err
}

func decodeRoadmapRoutine(envelope Envelope, payload roadmapRoutinePayload) (roadmap.ReviewRoutine, error) {
	threshold := payload.CommitThreshold != nil || payload.ElapsedSecondsThreshold != nil || payload.ChangedFilesThreshold != nil
	positive := (payload.CommitThreshold == nil || *payload.CommitThreshold > 0) && (payload.ElapsedSecondsThreshold == nil || *payload.ElapsedSecondsThreshold > 0) && (payload.ChangedFilesThreshold == nil || *payload.ChangedFilesThreshold > 0)
	if envelope.RecordRevision < 1 || envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || payload.OperationalSchemaVersion != roadmap.SchemaVersion || !validRoadmapText(payload.Title, true) || !oneOfValue(payload.Status, "proposed", "accepted", "superseded") || !threshold || !positive || !validRoadmapText(payload.Source, true) || !validRoadmapText(payload.SourceRef, false) {
		return roadmap.ReviewRoutine{}, fmt.Errorf("roadmap review routine %s/%d is invalid", envelope.RecordID, envelope.RecordRevision)
	}
	if !sameReferences(envelope.Refs, roadmapRoutineRefs(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Title, Status: payload.Status}) {
		return roadmap.ReviewRoutine{}, fmt.Errorf("roadmap review routine %s/%d metadata does not match payload", envelope.RecordID, envelope.RecordRevision)
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	return roadmap.ReviewRoutine{ID: envelope.RecordID, Revision: envelope.RecordRevision, Title: payload.Title, Status: payload.Status, CommitThreshold: payload.CommitThreshold, ElapsedSecondsThreshold: payload.ElapsedSecondsThreshold, ChangedFilesThreshold: payload.ChangedFilesThreshold, Source: payload.Source, SourceRef: payload.SourceRef, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}, err
}

func decodeRoadmapSync(envelope Envelope, payload roadmapSyncPayload) (roadmap.SyncRun, error) {
	if envelope.RecordRevision != 0 || envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || !validSyncHead(payload.GitCommit) || !validRoadmapText(payload.Client, false) || !validRoadmapText(payload.Model, true) || payload.InvocationID != "" && !portableIncomingIdentifier(payload.InvocationID) || len(payload.ReviewedNodes) == 0 || len(payload.ReviewedNodes) > 64 || len(payload.ReviewedFiles) == 0 || len(payload.ReviewedFiles) > 64 || payload.GitAuthority != "machine_observed" || !payload.SourceSnapshotOmitted || payload.OperationalSchemaVersion != roadmap.SchemaVersion {
		return roadmap.SyncRun{}, fmt.Errorf("roadmap sync %s is invalid", envelope.RecordID)
	}
	for _, nodeID := range payload.ReviewedNodes {
		if !portableIncomingIdentifier(nodeID) {
			return roadmap.SyncRun{}, fmt.Errorf("roadmap sync %s has an invalid reviewed node", envelope.RecordID)
		}
	}
	for _, path := range payload.ReviewedFiles {
		if !validRoadmapPath(path) {
			return roadmap.SyncRun{}, fmt.Errorf("roadmap sync %s has an invalid reviewed file", envelope.RecordID)
		}
	}
	if !sameReferences(envelope.Refs, roadmapSyncRefs(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Model, Status: "accepted", SourceCommit: payload.GitCommit}) {
		return roadmap.SyncRun{}, fmt.Errorf("roadmap sync %s metadata does not match payload", envelope.RecordID)
	}
	completed, err := parsePortableTime(envelope.RecordedAt)
	return roadmap.SyncRun{ID: envelope.RecordID, GitCommit: payload.GitCommit, Client: payload.Client, Model: payload.Model, InvocationID: payload.InvocationID, ReviewedNodes: payload.ReviewedNodes, ReviewedFiles: payload.ReviewedFiles, Authority: envelope.Authority, GitAuthority: payload.GitAuthority, CompletedAt: completed, SchemaVersion: payload.OperationalSchemaVersion}, err
}

func roadmapNodeRefs(payload roadmapNodePayload) []Reference {
	refs := []Reference{{Kind: "file", ID: payload.SourcePath}}
	if payload.SourceRef != "" {
		refs = append(refs, Reference{Kind: "source_ref", ID: payload.SourceRef})
	}
	return refs
}

func roadmapBindingRefs(payload roadmapBindingPayload) []Reference {
	return []Reference{{Kind: payload.PathKind, ID: payload.Path}, {Kind: "roadmap_node_revision", ID: roadmapRevisionID(payload.NodeID, payload.NodeRevision)}}
}

func roadmapLinkRefs(payload roadmapLinkPayload) []Reference {
	return []Reference{{Kind: "roadmap_node", ID: payload.TargetNodeID}, {Kind: "roadmap_node_revision", ID: roadmapRevisionID(payload.NodeID, payload.NodeRevision)}}
}

func roadmapRoutineRefs(payload roadmapRoutinePayload) []Reference {
	refs := []Reference{{Kind: "source", ID: payload.Source}}
	if payload.SourceRef != "" {
		refs = append(refs, Reference{Kind: "source_ref", ID: payload.SourceRef})
	}
	return refs
}

// validSyncHead accepts one commit, or a workspace watermark: member@commit
// entries sorted by member, each member a clean relative path.
func validSyncHead(value string) bool {
	if portableCommitPattern.MatchString(value) {
		return true
	}
	previous := ""
	for _, part := range strings.Split(value, ",") {
		member, commit, ok := strings.Cut(part, "@")
		if !ok || !validRoadmapPath(member) || strings.ContainsAny(member, "@,") || !portableCommitPattern.MatchString(commit) || member <= previous {
			return false
		}
		previous = member
	}
	return true
}

// syncHeadCommits lists the commits a sync watermark names.
func syncHeadCommits(value string) []string {
	if !roadmap.IsCompositeHead(value) {
		return []string{value}
	}
	var commits []string
	for _, part := range strings.Split(value, ",") {
		if _, commit, ok := strings.Cut(part, "@"); ok {
			commits = append(commits, commit)
		}
	}
	return commits
}

func roadmapSyncRefs(payload roadmapSyncPayload) []Reference {
	var refs []Reference
	for _, commit := range syncHeadCommits(payload.GitCommit) {
		refs = appendUniqueReference(refs, Reference{Kind: "commit", ID: commit})
	}
	if payload.InvocationID != "" {
		refs = append(refs, Reference{Kind: "actor_invocation", ID: payload.InvocationID})
	}
	for _, id := range payload.ReviewedNodes {
		refs = appendUniqueReference(refs, Reference{Kind: "roadmap_node", ID: id})
	}
	for _, path := range payload.ReviewedFiles {
		refs = appendUniqueReference(refs, Reference{Kind: "file", ID: path})
	}
	return refs
}

func appendUniqueReference(values []Reference, candidate Reference) []Reference {
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}

func roadmapRevisionID(id string, revision int) string { return fmt.Sprintf("%s@%d", id, revision) }

func roadmapRecordKey(id string, revision int) string { return fmt.Sprintf("%s\x00%d", id, revision) }

func portableRoadmapBase(id, authority string, schema int, projectID, projectRoot string) bool {
	return portableRoadmapIdentifier(id, projectID, projectRoot) && authority == "caller_asserted" && schema == roadmap.SchemaVersion
}

func portableRoadmapIdentifier(value, projectID, projectRoot string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value && portableRoadmapText(value, projectID, projectRoot) == value
}

func portableRoadmapText(value, projectID, projectRoot string) string {
	return strings.ReplaceAll(portableText(value, projectRoot), projectID, "[PROJECT_ID]")
}

func portableRoadmapStrings(values []string, projectID, projectRoot string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = portableRoadmapText(value, projectID, projectRoot)
	}
	return result
}

func validRoadmapText(value string, required bool) bool {
	if strings.TrimSpace(value) != value || required && value == "" || len(value) > roadmapTextMaxBytes || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	return redact.String(value) == value
}

func validRoadmapStrings(values []string) bool {
	for index, value := range values {
		if !validRoadmapText(value, true) || index > 0 && values[index-1] == value {
			return false
		}
	}
	return sort.StringsAreSorted(values)
}

func validRoadmapPath(value string) bool {
	return validRoadmapText(value, true) && value != "." && !filepath.IsAbs(value) && value != ".." && !strings.HasPrefix(value, "../") && filepath.ToSlash(filepath.Clean(value)) == value
}

func sortedRoadmapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
