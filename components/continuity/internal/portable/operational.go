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

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/fork"
	"github.com/llewm/llewm/components/continuity/internal/redact"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

type OperationalRecordSet struct {
	Identity          Identity
	DatabaseSchema    string
	Records           []EnvelopeSpec
	MissingReferences []Reference
}

type supersessionPayload struct {
	SuccessorID  string `json:"successor_id"`
	SupersedesID string `json:"supersedes_id"`
}

type workNarrativePayload struct {
	Source                   *worknarrative.Source  `json:"source,omitempty"`
	Title                    string                 `json:"title"`
	Trigger                  string                 `json:"trigger"`
	OccurredFrom             string                 `json:"occurred_from,omitempty"`
	OccurredTo               string                 `json:"occurred_to,omitempty"`
	Claims                   []worknarrative.Claim  `json:"claims"`
	Anchors                  []worknarrative.Anchor `json:"anchors"`
	Creator                  string                 `json:"creator"`
	CreatorInvocationID      string                 `json:"creator_invocation_id,omitempty"`
	OperationalSchemaVersion int                    `json:"operational_schema_version"`
}

type invocationPayload struct {
	SessionID                string              `json:"session_id,omitempty"`
	ParentInvocationID       string              `json:"parent_invocation_id,omitempty"`
	ActorType                string              `json:"actor_type"`
	Client                   string              `json:"client,omitempty"`
	ClientVersion            string              `json:"client_version,omitempty"`
	Provider                 string              `json:"provider,omitempty"`
	Model                    string              `json:"model,omitempty"`
	EffortRequested          *attribution.Effort `json:"effort_requested,omitempty"`
	EffortEffective          *attribution.Effort `json:"effort_effective,omitempty"`
	IdentitySource           string              `json:"identity_source"`
	OccurredAt               string              `json:"occurred_at"`
	ReceivedAt               string              `json:"received_at"`
	OperationalSchemaVersion int                 `json:"operational_schema_version"`
}

// LoadOperationalRecords maps the first real curated families into portable
// records inside one read transaction. Only invocations explicitly referenced by
// commit narratives are included.
func LoadOperationalRecords(ctx context.Context, db *sql.DB, projectID string) (OperationalRecordSet, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OperationalRecordSet{}, err
	}
	defer tx.Rollback()
	identity, err := identityFrom(ctx, tx, projectID)
	if err != nil {
		return OperationalRecordSet{}, err
	}
	projectRoot, err := projectRootFrom(ctx, tx, projectID)
	if err != nil {
		return OperationalRecordSet{}, err
	}
	var databaseSchema string
	if err := tx.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&databaseSchema); err != nil {
		return OperationalRecordSet{}, err
	}
	records, missing, err := loadOperationalSpecs(ctx, tx, projectID, projectRoot, identity.LineageID, false, nil)
	if err != nil {
		return OperationalRecordSet{}, err
	}
	if err := tx.Commit(); err != nil {
		return OperationalRecordSet{}, err
	}
	return OperationalRecordSet{Identity: identity, DatabaseSchema: databaseSchema, Records: records, MissingReferences: missing}, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func identityFrom(ctx context.Context, source queryer, projectID string) (Identity, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return Identity{}, fmt.Errorf("project ID is required")
	}
	var identity Identity
	var associated string
	err := source.QueryRowContext(ctx, `SELECT project_id,lineage_id,instance_id,association_source,authority,associated_at,schema_version FROM project_memory_identities WHERE project_id=?`, projectID).Scan(&identity.ProjectID, &identity.LineageID, &identity.InstanceID, &identity.AssociationSource, &identity.Authority, &associated, &identity.SchemaVersion)
	if err == sql.ErrNoRows {
		return Identity{}, fmt.Errorf("project portable identity is not initialized")
	}
	if err != nil {
		return Identity{}, err
	}
	identity.AssociatedAt, err = time.Parse(time.RFC3339Nano, associated)
	return identity, err
}

// projectRootFrom returns every location the project root has been observed at
// (member repositories stay visible as workspace-relative paths), joined by rootSeparator, so exports redact paths recorded before a move too.
func projectRootFrom(ctx context.Context, source queryer, projectID string) (string, error) {
	var root string
	if err := source.QueryRowContext(ctx, `SELECT root FROM projects WHERE id=?`, projectID).Scan(&root); err != nil {
		return "", err
	}
	rows, err := source.QueryContext(ctx, `SELECT DISTINCT l.path FROM repository_locations l JOIN repositories r ON r.id = l.repository_id AND r.role = 'primary'
        WHERE l.project_id=? AND l.path<>?`, projectID, root)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	roots := []string{root}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return "", err
		}
		roots = append(roots, path)
	}
	return strings.Join(roots, rootSeparator), rows.Err()
}

// rootSeparator cannot occur in a path, so one string can carry several roots
// through the export helpers without changing their signatures.
const rootSeparator = "\x00"

func loadOperationalSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string, allInvocations bool, validated snapshotEncodings) ([]EnvelopeSpec, []Reference, error) {
	checkpointSpecs, err := loadCheckpointSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	roadmapSpecs, roadmapInvocations, err := loadRoadmapSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	releaseSpecs, err := loadReleaseSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	reviewSpecs, err := loadReviewSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	trackPlanSpecs, err := loadTrackPlanSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	ledgerSpecs, err := loadLedgerSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	procedureSpecs, err := loadProcedureSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	resourceSpecs, err := loadResourceSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	pinSpecs, err := loadPinSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	coverageSpecs, err := loadCoverageCellSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	leaseSpecs, err := loadLeaseSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	usageSpecs, err := loadUsageSummarySpecs(ctx, source, projectID, lineageID)
	if err != nil {
		return nil, nil, err
	}
	contextSpecs, err := loadCheckpointContextSpecs(ctx, source, projectID, lineageID)
	if err != nil {
		return nil, nil, err
	}
	branchSpecs, err := loadCheckpointBranchesSpecs(ctx, source, projectID, lineageID)
	if err != nil {
		return nil, nil, err
	}
	resolutionSpecs, err := loadForkResolutionSpecs(ctx, source, projectID, projectRoot, lineageID)
	if err != nil {
		return nil, nil, err
	}
	records := append([]EnvelopeSpec(nil), checkpointSpecs...)
	records = append(records, roadmapSpecs...)
	records = append(records, releaseSpecs...)
	records = append(records, reviewSpecs...)
	records = append(records, trackPlanSpecs...)
	records = append(records, ledgerSpecs...)
	records = append(records, procedureSpecs...)
	records = append(records, resourceSpecs...)
	records = append(records, pinSpecs...)
	records = append(records, coverageSpecs...)
	records = append(records, leaseSpecs...)
	records = append(records, usageSpecs...)
	records = append(records, contextSpecs...)
	records = append(records, branchSpecs...)
	records = append(records, resolutionSpecs...)
	recoverySpecs, err := loadRedactionRecoverySpecs(ctx, source, projectID)
	if err != nil {
		return nil, nil, err
	}
	records = append(records, recoverySpecs...)
	invocationIDs := roadmapInvocations
	// Retained narrative envelopes are immutable. We still read and check the
	// operational payload and its invocation binding, but need not reconstruct
	// and redact an envelope that retainedSpecs will replace with those bytes.
	retainedNarratives := make(map[string]bool)
	retainedRows, err := source.QueryContext(ctx, `SELECT record_type,record_id FROM portable_envelopes WHERE project_id=? AND record_revision=0 AND record_type IN ('work_narrative','work_narrative_supersession')`, projectID)
	if err != nil {
		return nil, nil, err
	}
	for retainedRows.Next() {
		var kind, id string
		if err := retainedRows.Scan(&kind, &id); err != nil {
			retainedRows.Close()
			return nil, nil, err
		}
		retainedNarratives[RecordKey(kind, id, 0)] = true
	}
	if err := retainedRows.Err(); err != nil {
		retainedRows.Close()
		return nil, nil, err
	}
	if err := retainedRows.Close(); err != nil {
		return nil, nil, err
	}
	workRows, err := source.QueryContext(ctx, `SELECT payload_json,COALESCE(creator_invocation_id,'') FROM work_narratives WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer workRows.Close()
	for workRows.Next() {
		var raw, creatorInvocationID string
		if err := workRows.Scan(&raw, &creatorInvocationID); err != nil {
			return nil, nil, err
		}
		var record worknarrative.Record
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, nil, fmt.Errorf("decode work narrative payload: %w", err)
		}
		if record.ProjectID != projectID || record.RecordType != worknarrative.RecordType || record.SchemaVersion != worknarrative.SchemaVersion {
			return nil, nil, fmt.Errorf("work narrative payload identity or schema mismatch for %q", record.ID)
		}
		record.CreatorInvocationID = creatorInvocationID
		if retainedNarratives[RecordKey("work_narrative", record.ID, 0)] && (record.SupersedesID == "" || retainedNarratives[RecordKey("work_narrative_supersession", record.ID, 0)]) {
			records = append(records, EnvelopeSpec{RecordType: "work_narrative", RecordID: record.ID})
			if record.SupersedesID != "" {
				records = append(records, EnvelopeSpec{RecordType: "work_narrative_supersession", RecordID: record.ID})
			}
			if creatorInvocationID != "" {
				invocationIDs[creatorInvocationID] = struct{}{}
			}
			continue
		}
		if record.Source != nil {
			v := *record.Source
			v.Publisher = portableText(v.Publisher, projectRoot)
			v.Version = portableText(v.Version, projectRoot)
			record.Source = &v
		}
		claims := append([]worknarrative.Claim{}, record.Claims...)
		for i := range claims {
			claims[i].Text = portableText(claims[i].Text, projectRoot)
			claims[i].Citations = append([]worknarrative.Citation(nil), claims[i].Citations...)
			for j := range claims[i].Citations {
				claims[i].Citations[j].Locator = portableText(claims[i].Citations[j].Locator, projectRoot)
			}
		}
		anchors := append([]worknarrative.Anchor{}, record.Anchors...)
		for i := range anchors {
			anchors[i].Target = portableText(anchors[i].Target, projectRoot)
			anchors[i].TargetRevision = portableText(anchors[i].TargetRevision, projectRoot)
			anchors[i].RegionLocator = portableText(anchors[i].RegionLocator, projectRoot)
			anchors[i].Drift = ""
		}
		payload, err := json.Marshal(workNarrativePayload{Source: record.Source, Title: portableText(record.Title, projectRoot), Trigger: record.Trigger, OccurredFrom: formatOptionalPortableTime(record.OccurredFrom), OccurredTo: formatOptionalPortableTime(record.OccurredTo), Claims: claims, Anchors: anchors, Creator: portableText(record.Creator, projectRoot), CreatorInvocationID: record.CreatorInvocationID, OperationalSchemaVersion: record.SchemaVersion})
		if err != nil {
			return nil, nil, err
		}
		taskID := ""
		for _, anchor := range anchors {
			if anchor.Kind == "task" && taskID == "" {
				taskID = anchor.Target
			}
		}
		refs := workNarrativeReferences(anchors, record.CreatorInvocationID, claims)
		if record.CreatorInvocationID != "" {
			invocationIDs[record.CreatorInvocationID] = struct{}{}
		}
		// Supersession state is left out for the same reason as commit narratives.
		index, err := json.Marshal(envelopeIndex{Title: portableText(record.Title, projectRoot), TaskID: taskID})
		if err != nil {
			return nil, nil, err
		}
		records = append(records, EnvelopeSpec{LineageID: lineageID, RecordType: "work_narrative", RecordSchemaVersion: 1, RecordID: record.ID, RecordedAt: record.RecordedAt, Authority: "mixed", Index: index, Refs: refs, Payload: payload})
		if record.SupersedesID != "" {
			edge, _ := json.Marshal(supersessionPayload{SuccessorID: record.ID, SupersedesID: record.SupersedesID})
			records = append(records, EnvelopeSpec{LineageID: lineageID, RecordType: "work_narrative_supersession", RecordSchemaVersion: 1, RecordID: record.ID, RecordedAt: record.RecordedAt, Authority: "mixed", Refs: []Reference{{Kind: "narrative", ID: record.ID}, {Kind: "narrative", ID: record.SupersedesID}}, Payload: edge})
		}
	}
	if err := workRows.Err(); err != nil {
		return nil, nil, err
	}
	invocations, err := loadInvocations(ctx, source, projectID, invocationIDs, allInvocations)
	if err != nil {
		return nil, nil, err
	}
	found := make(map[string]struct{}, len(invocations))
	// Invocation metadata repeats heavily across a session. Keep only a small,
	// per-snapshot cache; arbitrary text and redaction results are never persisted.
	invocationText := cachedPortableText(projectRoot)
	for _, invocation := range invocations {
		found[invocation.ID] = struct{}{}
		if invocation.ParentInvocationID != "" {
			invocationIDs[invocation.ParentInvocationID] = struct{}{}
		}
		spec, err := invocationSpec(lineageID, invocationText, invocation)
		if err != nil {
			return nil, nil, err
		}
		records = append(records, spec)
	}
	missing := make([]Reference, 0)
	for id := range invocationIDs {
		if _, exists := found[id]; !exists {
			missing = append(missing, Reference{Kind: "actor_invocation", ID: id})
		}
	}
	sort.Slice(missing, func(i, j int) bool { return lessUTF16(missing[i].ID, missing[j].ID) })
	records, err = retainedSpecs(ctx, source, projectID, lineageID, records, validated)
	return records, missing, err
}

func formatOptionalPortableTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func loadInvocations(ctx context.Context, source queryer, projectID string, ids map[string]struct{}, all bool) ([]attribution.Invocation, error) {
	if all {
		return loadInvocationBatch(ctx, source, projectID, ids, true)
	}
	pending := map[string]struct{}{}
	for id := range ids {
		pending[id] = struct{}{}
	}
	seen := map[string]bool{}
	var result []attribution.Invocation
	for len(pending) > 0 {
		ordered := make([]string, 0, len(pending))
		for id := range pending {
			ordered = append(ordered, id)
		}
		sort.Strings(ordered)
		if len(ordered) > 512 {
			ordered = ordered[:512]
		}
		if len(seen)+len(ordered) > MaxRecordCount {
			return nil, fmt.Errorf("invocation dependency count exceeds bounds")
		}
		batch := map[string]struct{}{}
		for _, id := range ordered {
			delete(pending, id)
			seen[id] = true
			batch[id] = struct{}{}
		}
		values, err := loadInvocationBatch(ctx, source, projectID, batch, false)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			result = append(result, value)
			if value.ParentInvocationID != "" && !seen[value.ParentInvocationID] {
				pending[value.ParentInvocationID] = struct{}{}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].OccurredAt.Before(result[j].OccurredAt)
	})
	return result, nil
}

func loadInvocationBatch(ctx context.Context, source queryer, projectID string, ids map[string]struct{}, all bool) ([]attribution.Invocation, error) {
	args := []any{projectID}
	query := `SELECT id,session_id,parent_invocation_id,actor_type,client,client_version,provider,model,identity_source,occurred_at,received_at,schema_version,effort_requested_raw,effort_requested_normalized,effort_requested_source,effort_requested_authority,effort_effective_raw,effort_effective_normalized,effort_effective_source,effort_effective_authority FROM actor_invocations WHERE project_id=?`
	if !all {
		if len(ids) == 0 {
			return []attribution.Invocation{}, nil
		}
		ordered := make([]string, 0, len(ids))
		for id := range ids {
			ordered = append(ordered, id)
		}
		sort.Strings(ordered)
		query += " AND id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ordered)), ",") + ")"
		for _, id := range ordered {
			args = append(args, id)
		}
	}
	query += " ORDER BY occurred_at,id"
	rows, err := source.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]attribution.Invocation, 0)
	for rows.Next() {
		var value attribution.Invocation
		var session, parent, client, clientVersion, provider, model sql.NullString
		var requestedRaw, requestedNormalized, requestedSource, requestedAuthority sql.NullString
		var effectiveRaw, effectiveNormalized, effectiveSource, effectiveAuthority sql.NullString
		var occurred, received string
		if err := rows.Scan(&value.ID, &session, &parent, &value.ActorType, &client, &clientVersion, &provider, &model, &value.IdentitySource, &occurred, &received, &value.SchemaVersion, &requestedRaw, &requestedNormalized, &requestedSource, &requestedAuthority, &effectiveRaw, &effectiveNormalized, &effectiveSource, &effectiveAuthority); err != nil {
			return nil, err
		}
		value.ProjectID = projectID
		value.SessionID, value.ParentInvocationID = nullString(session), nullString(parent)
		value.Client, value.ClientVersion = nullString(client), nullString(clientVersion)
		value.Provider, value.Model = nullString(provider), nullString(model)
		value.EffortRequested = portableEffort(requestedRaw, requestedNormalized, requestedSource, requestedAuthority)
		value.EffortEffective = portableEffort(effectiveRaw, effectiveNormalized, effectiveSource, effectiveAuthority)
		value.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err == nil {
			value.ReceivedAt, err = time.Parse(time.RFC3339Nano, received)
		}
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func invocationSpec(lineageID string, text func(string) string, value attribution.Invocation) (EnvelopeSpec, error) {
	payload, err := json.Marshal(invocationPayload{
		SessionID: value.SessionID, ParentInvocationID: value.ParentInvocationID,
		ActorType: text(value.ActorType), Client: text(value.Client), ClientVersion: text(value.ClientVersion),
		Provider: text(value.Provider), Model: text(value.Model), EffortRequested: portableEffortText(value.EffortRequested, text),
		EffortEffective: portableEffortText(value.EffortEffective, text), IdentitySource: value.IdentitySource,
		OccurredAt: value.OccurredAt.UTC().Format(time.RFC3339Nano), ReceivedAt: value.ReceivedAt.UTC().Format(time.RFC3339Nano),
		OperationalSchemaVersion: value.SchemaVersion,
	})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	refs := make([]Reference, 0, 2)
	if value.SessionID != "" {
		refs = append(refs, Reference{Kind: "session", ID: value.SessionID})
	}
	if value.ParentInvocationID != "" {
		refs = append(refs, Reference{Kind: "actor_invocation", ID: value.ParentInvocationID})
	}
	index, err := json.Marshal(envelopeIndex{Title: text(value.Model)})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	return EnvelopeSpec{LineageID: lineageID, RecordType: "actor_invocation", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.OccurredAt, Authority: value.IdentitySource, Index: index, Refs: refs, Payload: payload}, nil
}

func portableEffortText(value *attribution.Effort, text func(string) string) *attribution.Effort {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Raw = text(copy.Raw)
	copy.Normalized = text(copy.Normalized)
	copy.Source = text(copy.Source)
	copy.Authority = text(copy.Authority)
	return &copy
}

func portableText(value, projectRoot string) string {
	value = redact.String(value)
	roots := strings.Split(projectRoot, rootSeparator)
	// Replace longer roots first so a nested former location is not partially replaced.
	sort.SliceStable(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	for _, candidate := range roots {
		root := filepath.Clean(candidate)
		if root == "." || root == string(filepath.Separator) {
			continue
		}
		value = strings.ReplaceAll(value, root, "[PROJECT_ROOT]")
		slashRoot := filepath.ToSlash(root)
		if slashRoot != root {
			value = strings.ReplaceAll(value, slashRoot, "[PROJECT_ROOT]")
		}
	}
	return value
}

func cachedPortableText(projectRoot string) func(string) string {
	cache := make(map[string]string)
	return func(value string) string {
		if result, ok := cache[value]; ok {
			return result
		}
		result := portableText(value, projectRoot)
		if len(cache) < 256 && len(value) <= 256 {
			cache[value] = result
		}
		return result
	}
}

func portableEffort(raw, normalized, source, authority sql.NullString) *attribution.Effort {
	if !raw.Valid {
		return nil
	}
	return &attribution.Effort{Raw: raw.String, Normalized: nullString(normalized), Source: nullString(source), Authority: nullString(authority)}
}

func nullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func nonNilStrings(values []string) []string {
	copy := append([]string{}, values...)
	return copy
}

type PlanDisposition string

const (
	PlanAppend         PlanDisposition = "append"
	PlanAlreadyPresent PlanDisposition = "already_present"
	PlanCollision      PlanDisposition = "collision"
	PlanUnsupported    PlanDisposition = "unsupported"
	PlanInvalid        PlanDisposition = "invalid"
)

type ImportPlanItem struct {
	RecordType             string          `json:"record_type"`
	RecordID               string          `json:"record_id"`
	RecordRevision         int             `json:"record_revision"`
	Disposition            PlanDisposition `json:"disposition"`
	IncomingEnvelopeSHA256 string          `json:"incoming_envelope_sha256"`
	ExistingEnvelopeSHA256 string          `json:"existing_envelope_sha256,omitempty"`
	Reason                 string          `json:"reason,omitempty"`
}

type ImportPlan struct {
	LineageID       string                  `json:"lineage_id"`
	ProjectID       string                  `json:"project_id"`
	Counts          map[PlanDisposition]int `json:"counts"`
	Items           []ImportPlanItem        `json:"items"`
	ApplyAllowed    bool                    `json:"apply_allowed"`
	WritesPerformed int                     `json:"writes_performed"`
}

func PlanOperationalImport(ctx context.Context, db *sql.DB, projectID string, manifestBytes, recordsBytes []byte) (ImportPlan, error) {
	return planOperationalImport(ctx, db, projectID, manifestBytes, recordsBytes, nil)
}

type transactionStarter interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func planOperationalImport(ctx context.Context, db transactionStarter, projectID string, manifestBytes, recordsBytes []byte, snapshot *map[string]EncodedEnvelope) (ImportPlan, error) {
	bundle, err := ValidateBundle(manifestBytes, recordsBytes)
	if err != nil {
		return invalidImportPlan(projectID, err), nil
	}
	decoded, err := decodeOperationalBundle(bundle)
	if err != nil {
		return invalidImportPlan(projectID, err), nil
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ImportPlan{}, err
	}
	defer tx.Rollback()
	plan, err := planValidatedOperationalImport(ctx, tx, projectID, bundle, snapshot)
	if err != nil {
		return ImportPlan{}, err
	}
	if plan.ApplyAllowed {
		appendKeys := planAppendKeys(plan)
		if err := validateApplyDependencies(ctx, tx, projectID, decoded, appendKeys); err != nil {
			return invalidImportPlan(projectID, err), nil
		}
	}
	if err := tx.Commit(); err != nil {
		return ImportPlan{}, err
	}
	return plan, nil
}

func planAppendKeys(plan ImportPlan) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, item := range plan.Items {
		if item.Disposition == PlanAppend {
			keys[planItemKey(item)] = struct{}{}
		}
	}
	return keys
}

func planValidatedOperationalImport(ctx context.Context, source queryer, projectID string, bundle Bundle, snapshot *map[string]EncodedEnvelope) (ImportPlan, error) {
	identity, err := identityFrom(ctx, source, projectID)
	if err != nil {
		return ImportPlan{}, err
	}
	if identity.LineageID != bundle.Manifest.LineageID {
		return ImportPlan{}, fmt.Errorf("manifest lineage does not match selected project")
	}
	projectRoot, err := projectRootFrom(ctx, source, projectID)
	if err != nil {
		return ImportPlan{}, err
	}
	validated := make(snapshotEncodings)
	localSpecs, _, err := loadOperationalSpecs(ctx, source, projectID, projectRoot, identity.LineageID, true, validated)
	if err != nil {
		return ImportPlan{}, err
	}
	local := make(map[string]EncodedEnvelope, len(localSpecs))
	for _, spec := range localSpecs {
		encoded, err := validated.encode(spec)
		if err != nil {
			return ImportPlan{}, err
		}
		local[recordKey(encoded.Envelope)] = encoded
	}
	counts := emptyPlanCounts()
	if snapshot != nil {
		*snapshot = local
	}
	items := make([]ImportPlanItem, 0, len(bundle.Records))
	applyAllowed := true
	for _, incoming := range bundle.Records {
		item := ImportPlanItem{RecordType: incoming.Envelope.RecordType, RecordID: incoming.Envelope.RecordID, RecordRevision: incoming.Envelope.RecordRevision, IncomingEnvelopeSHA256: incoming.Envelope.EnvelopeSHA256}
		switch incoming.Envelope.RecordType {
		case "redaction_recovery", "procedure", "resource", "resource_note", "pin", "coverage_cell", "lease_stamp", "stale_write_resolution", "usage_session_summary", "checkpoint_context", "checkpoint_branches", "fork_resolution", "revision_fork_resolution", "supersession_fork_resolution", "checkpoint", "checkpoint_action", "checkpoint_action_transition", "roadmap_node", "roadmap_binding", "roadmap_link", "roadmap_review_routine", "roadmap_sync", "roadmap_release", "work_item", "work_item_disposition", "work_item_link", "track_plan", "work_narrative", "work_narrative_supersession", "actor_invocation", "decision", "topic_revision", "topic_alias", "topic_link":
			existing, found := local[recordKey(incoming.Envelope)]
			item.ExistingEnvelopeSHA256 = existing.Envelope.EnvelopeSHA256
			if !found {
				item.Disposition = PlanAppend
			} else if existing.Envelope.EnvelopeSHA256 == incoming.Envelope.EnvelopeSHA256 {
				item.Disposition = PlanAlreadyPresent
			} else if fork.EquivalentRoadmapNodeEnvelopes([][]byte{existing.Bytes, incoming.Bytes}) {
				// A full format conversion replays original schema observations.
				// Keep those bytes as evidence without colliding with the already
				// accepted semantic node or rewriting its operational form.
				item.Disposition, item.Reason = PlanAlreadyPresent, "equivalent roadmap schema observation"
			} else {
				item.Disposition, item.Reason, applyAllowed = PlanCollision, "same portable identity has different envelope hash", false
			}
		default:
			item.Disposition, item.Reason, applyAllowed = PlanUnsupported, "operational importer does not support this record family yet", false
		}
		counts[item.Disposition]++
		items = append(items, item)
	}
	return ImportPlan{LineageID: identity.LineageID, ProjectID: projectID, Counts: counts, Items: items, ApplyAllowed: applyAllowed, WritesPerformed: 0}, nil
}

func invalidImportPlan(projectID string, err error) ImportPlan {
	counts := emptyPlanCounts()
	counts[PlanInvalid] = 1
	return ImportPlan{
		ProjectID: strings.TrimSpace(projectID), Counts: counts,
		Items:           []ImportPlanItem{{Disposition: PlanInvalid, Reason: err.Error()}},
		ApplyAllowed:    false,
		WritesPerformed: 0,
	}
}

func emptyPlanCounts() map[PlanDisposition]int {
	return map[PlanDisposition]int{
		PlanAppend: 0, PlanAlreadyPresent: 0, PlanCollision: 0,
		PlanUnsupported: 0, PlanInvalid: 0,
	}
}

func recordKey(value Envelope) string {
	return fmt.Sprintf("%s\x00%s\x00%d", value.RecordType, value.RecordID, value.RecordRevision)
}
