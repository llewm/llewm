package portable

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/release"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

var portableCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
var portableHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ImportReceipt struct {
	ID                    string    `json:"id"`
	ProjectID             string    `json:"project_id"`
	LineageID             string    `json:"lineage_id"`
	ExportID              string    `json:"export_id"`
	SourceInstanceID      string    `json:"source_instance_id"`
	ManifestSHA256        string    `json:"manifest_sha256"`
	RecordsSHA256         string    `json:"records_sha256"`
	RecordSetSHA256       string    `json:"record_set_sha256"`
	SelectionProfile      string    `json:"selection_profile"`
	RedactionProfile      string    `json:"redaction_profile"`
	RedactionVersion      int       `json:"redaction_version"`
	RedactionSHA256       string    `json:"redaction_sha256"`
	AppendedRecords       int       `json:"appended_records"`
	AlreadyPresentRecords int       `json:"already_present_records"`
	AppliedAt             time.Time `json:"applied_at"`
	Authority             string    `json:"authority"`
	SchemaVersion         int       `json:"schema_version"`
}

type ImportApplyResult struct {
	Plan    ImportPlan     `json:"plan"`
	Receipt *ImportReceipt `json:"receipt,omitempty"`
}

type decodedOperationalBundle struct {
	checkpoints    map[string]checkpoint.Checkpoint
	actions        map[string]portableCheckpointAction
	transitions    map[string]portableCheckpointTransition
	roadmap        portableRoadmapRecords
	releases       map[string]release.Record
	reviews        portableReviewRecords
	trackPlans     portableTrackPlanRecords
	invocations    map[string]attribution.Invocation
	workNarratives map[string]worknarrative.Record
	workEdges      map[string]decodedSupersession
	ledger         portableLedgerRecords
	procedures     portableProcedures
	resources      portableResources
	pins           portablePins
	coverageCells  portableCoverageCells
	leases         portableLeaseRecords
	usageSummaries portableUsageSummaries
	contexts       portableCheckpointContexts
	branches       portableCheckpointBranches
	resolutions    portableForkResolutions
}

type decodedSupersession struct {
	payload    supersessionPayload
	authority  string
	recordedAt string
}

// ApplyOperationalImport validates and plans again inside the write transaction,
// then appends supported semantic records and one receipt atomically.
func ApplyOperationalImport(ctx context.Context, db *sql.DB, projectID string, manifestBytes, recordsBytes []byte, appliedAt time.Time) (ImportApplyResult, error) {
	return applyOperationalImport(ctx, db, projectID, manifestBytes, recordsBytes, appliedAt, nil)
}

// applyOperationalImport applies the bundle and then runs within, when set, in
// the same transaction, so a caller's own writes commit or roll back with it.
func applyOperationalImport(ctx context.Context, db *sql.DB, projectID string, manifestBytes, recordsBytes []byte, appliedAt time.Time, within func(context.Context, *sql.Tx) error) (ImportApplyResult, error) {
	return applyOperationalImportWithPlan(ctx, db, projectID, manifestBytes, recordsBytes, appliedAt, within, nil)
}

type reusableImportPlan struct {
	plan         ImportPlan
	dataVersion  int64
	projectID    string
	manifestHash [32]byte
	recordsHash  [32]byte
}

func newReusableImportPlan(plan ImportPlan, dataVersion int64, projectID string, manifest, records []byte) *reusableImportPlan {
	return &reusableImportPlan{plan: plan, dataVersion: dataVersion, projectID: projectID, manifestHash: sha256.Sum256(manifest), recordsHash: sha256.Sum256(records)}
}

func applyOperationalImportWithPlan(ctx context.Context, db transactionStarter, projectID string, manifestBytes, recordsBytes []byte, appliedAt time.Time, within func(context.Context, *sql.Tx) error, previous *reusableImportPlan) (ImportApplyResult, error) {
	bundle, err := ValidateBundle(manifestBytes, recordsBytes)
	if err != nil {
		plan := invalidImportPlan(projectID, err)
		return ImportApplyResult{Plan: plan}, fmt.Errorf("cannot apply invalid portable bundle: %w", err)
	}
	decoded, err := decodeOperationalBundle(bundle)
	if err != nil {
		plan := invalidImportPlan(projectID, err)
		return ImportApplyResult{Plan: plan}, fmt.Errorf("cannot apply invalid operational records: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ImportApplyResult{}, err
	}
	defer tx.Rollback()
	var plan ImportPlan
	var dataVersion int64
	if previous != nil && previous.projectID == projectID && previous.manifestHash == sha256.Sum256(manifestBytes) && previous.recordsHash == sha256.Sum256(recordsBytes) && tx.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&dataVersion) == nil && dataVersion == previous.dataVersion {
		plan = previous.plan
	} else {
		plan, err = planValidatedOperationalImport(ctx, tx, projectID, bundle, nil)
		if err != nil {
			return ImportApplyResult{}, err
		}
	}
	result := ImportApplyResult{Plan: plan}
	if !plan.ApplyAllowed {
		return result, fmt.Errorf("portable import plan is not applicable")
	}
	appendKeys := planAppendKeys(plan)
	if err := validateApplyDependencies(ctx, tx, projectID, decoded, appendKeys); err != nil {
		return result, err
	}
	if err := appendCheckpoints(ctx, tx, projectID, decoded, appendKeys); err != nil {
		return result, err
	}
	if err := appendCheckpointContexts(ctx, tx, projectID, decoded.contexts, appendKeys); err != nil {
		return result, err
	}
	if err := appendCheckpointBranches(ctx, tx, projectID, decoded.branches, appendKeys); err != nil {
		return result, err
	}
	if err := appendForkResolutions(ctx, tx, projectID, decoded.resolutions, appendKeys); err != nil {
		return result, err
	}
	if err := appendRoadmapGraph(ctx, tx, projectID, decoded.roadmap, appendKeys); err != nil {
		return result, err
	}
	if err := appendReviewRecords(ctx, tx, projectID, decoded.reviews, appendKeys); err != nil {
		return result, err
	}
	if err := appendInvocations(ctx, tx, projectID, decoded.invocations, appendKeys); err != nil {
		return result, err
	}
	if err := appendReleaseRecords(ctx, tx, projectID, decoded.releases, appendKeys); err != nil {
		return result, err
	}
	if err := appendTrackPlanRecords(ctx, tx, projectID, decoded.trackPlans, appendKeys); err != nil {
		return result, err
	}
	if err := appendLedgerRecords(ctx, tx, projectID, decoded.ledger, appendKeys); err != nil {
		return result, err
	}
	if err := appendProcedures(ctx, tx, projectID, decoded.procedures, appendKeys); err != nil {
		return result, err
	}
	if err := appendResources(ctx, tx, projectID, decoded.resources, appendKeys); err != nil {
		return result, err
	}
	if err := appendPins(ctx, tx, projectID, decoded.pins, appendKeys); err != nil {
		return result, err
	}
	if err := appendCoverageCells(ctx, tx, projectID, decoded.coverageCells, appendKeys); err != nil {
		return result, err
	}
	if err := appendLeaseRecords(ctx, tx, projectID, decoded.leases, appendKeys); err != nil {
		return result, err
	}
	if err := appendUsageSummaries(ctx, tx, projectID, decoded.usageSummaries, appendKeys); err != nil {
		return result, err
	}
	if err := appendRoadmapSyncs(ctx, tx, projectID, decoded.roadmap.Syncs, appendKeys); err != nil {
		return result, err
	}
	if err := appendWorkNarratives(ctx, tx, projectID, decoded, appendKeys); err != nil {
		return result, err
	}
	// Retain the exact incoming form atomically with its operational rows. Do
	// not adopt a differing schema-equivalent observation over a local record.
	for _, encoded := range bundle.Records {
		if _, appended := appendKeys[recordKey(encoded.Envelope)]; appended {
			if encoded.Envelope.RecordType == "redaction_recovery" {
				if err := appendRedactionRecovery(ctx, tx, projectID, encoded); err != nil {
					return result, err
				}
			}
			if err := retainEnvelope(ctx, tx, projectID, "import", encoded); err != nil {
				return result, err
			}
		}
	}
	// Check relationships between all retained proofs inside the transaction.
	// A malformed chain must never strand the store after a successful import.
	if _, err := RecoveredEnvelopes(ctx, tx, projectID); err != nil {
		return result, err
	}
	if appliedAt.IsZero() {
		appliedAt = time.Now().UTC()
	} else {
		appliedAt = appliedAt.UTC()
	}
	receipt := ImportReceipt{
		ID: activity.NewID(), ProjectID: projectID, LineageID: bundle.Manifest.LineageID,
		ExportID: bundle.Manifest.ExportID, SourceInstanceID: bundle.Manifest.SourceInstanceID,
		ManifestSHA256: hashHex(bundle.ManifestBytes), RecordsSHA256: bundle.Manifest.RecordsSHA256,
		RecordSetSHA256: bundle.Manifest.RecordSetSHA256, SelectionProfile: bundle.Manifest.SelectionProfile,
		RedactionProfile: bundle.Manifest.RedactionProfile.ID, RedactionVersion: bundle.Manifest.RedactionProfile.Version,
		RedactionSHA256: bundle.Manifest.RedactionProfile.SHA256,
		AppendedRecords: plan.Counts[PlanAppend], AlreadyPresentRecords: plan.Counts[PlanAlreadyPresent],
		AppliedAt: appliedAt, Authority: "machine_observed", SchemaVersion: 1,
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO portable_import_receipts(id,project_id,lineage_id,export_id,source_instance_id,manifest_sha256,records_sha256,record_set_sha256,selection_profile,redaction_profile_id,redaction_profile_version,redaction_profile_sha256,appended_records,already_present_records,applied_at,authority,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.ID, receipt.ProjectID, receipt.LineageID, receipt.ExportID, receipt.SourceInstanceID, receipt.ManifestSHA256, receipt.RecordsSHA256, receipt.RecordSetSHA256, receipt.SelectionProfile, receipt.RedactionProfile, receipt.RedactionVersion, receipt.RedactionSHA256, receipt.AppendedRecords, receipt.AlreadyPresentRecords, receipt.AppliedAt.Format(time.RFC3339Nano), receipt.Authority, receipt.SchemaVersion)
	if err != nil {
		return result, fmt.Errorf("append portable import receipt: %w", err)
	}
	if within != nil {
		if err := within(ctx, tx); err != nil {
			return result, err
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	plan.WritesPerformed = receipt.AppendedRecords + 1
	result.Plan = plan
	result.Receipt = &receipt
	return result, nil
}

func decodeOperationalBundle(bundle Bundle) (decodedOperationalBundle, error) {
	decoded := decodedOperationalBundle{
		checkpoints: make(map[string]checkpoint.Checkpoint), actions: make(map[string]portableCheckpointAction), transitions: make(map[string]portableCheckpointTransition),
		roadmap:     newPortableRoadmapRecords(),
		releases:    make(map[string]release.Record),
		reviews:     newPortableReviewRecords(),
		trackPlans:  newPortableTrackPlanRecords(),
		invocations: make(map[string]attribution.Invocation), workNarratives: make(map[string]worknarrative.Record), workEdges: make(map[string]decodedSupersession),
		ledger:         newPortableLedgerRecords(),
		procedures:     portableProcedures{},
		resources:      newPortableResources(),
		pins:           portablePins{},
		coverageCells:  portableCoverageCells{},
		leases:         newPortableLeaseRecords(),
		usageSummaries: portableUsageSummaries{},
		contexts:       portableCheckpointContexts{},
		branches:       portableCheckpointBranches{},
		resolutions:    portableForkResolutions{},
	}
	for _, encoded := range bundle.Records {
		envelope := encoded.Envelope
		if envelope.RecordType == "redaction_recovery" {
			if _, err := decodeRedactionRecovery(encoded); err != nil {
				return decoded, err
			}
			continue
		}
		if handled, err := decodeLedgerEnvelope(&decoded.ledger, envelope); handled {
			if err != nil {
				return decoded, err
			}
			continue
		}
		if handled, err := decodeProcedureEnvelope(decoded.procedures, envelope); handled {
			if err != nil {
				return decoded, err
			}
			continue
		}
		if handled, err := decodeResourceEnvelope(decoded.resources, envelope); handled {
			if err != nil {
				return decoded, err
			}
			continue
		}
		if handled, err := decodeForkResolutionEnvelope(decoded.resolutions, envelope); handled {
			if err != nil {
				return decoded, err
			}
			continue
		}
		if handled, err := decodeCheckpointContextEnvelope(decoded.contexts, envelope); handled {
			if err != nil {
				return decoded, err
			}
			continue
		}
		if handled, err := decodeCheckpointBranchesEnvelope(decoded.branches, envelope); handled {
			if err != nil {
				return decoded, err
			}
			continue
		}
		if handled, err := decodePinEnvelope(decoded.pins, envelope); handled {
			if err != nil {
				return decoded, err
			}
			continue
		}
		if handled, err := decodeCoverageCellEnvelope(decoded.coverageCells, envelope); handled {
			if err != nil {
				return decoded, err
			}
			continue
		}
		if handled, err := decodeLeaseEnvelope(decoded.leases, envelope); handled {
			if err != nil {
				return decoded, err
			}
			continue
		}
		if handled, err := decodeUsageSummaryEnvelope(decoded.usageSummaries, envelope); handled {
			if err != nil {
				return decoded, err
			}
			continue
		}
		if envelope.RecordRevision != 0 && envelope.RecordType != "roadmap_node" && envelope.RecordType != "roadmap_review_routine" && envelope.RecordType != "roadmap_release" && envelope.RecordType != "track_plan" {
			return decoded, fmt.Errorf("operational record %s/%s uses unsupported revision %d", envelope.RecordType, envelope.RecordID, envelope.RecordRevision)
		}
		switch envelope.RecordType {
		case "roadmap_node":
			var payload roadmapNodePayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode roadmap node %s/%d: %w", envelope.RecordID, envelope.RecordRevision, err)
			}
			value, err := decodeRoadmapNode(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.roadmap.Nodes[roadmapRecordKey(envelope.RecordID, envelope.RecordRevision)] = value
		case "roadmap_binding":
			var payload roadmapBindingPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode roadmap binding %s: %w", envelope.RecordID, err)
			}
			value, err := decodeRoadmapBinding(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.roadmap.Bindings[envelope.RecordID] = value
		case "roadmap_link":
			var payload roadmapLinkPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode roadmap link %s: %w", envelope.RecordID, err)
			}
			value, err := decodeRoadmapLink(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.roadmap.Links[envelope.RecordID] = value
		case "roadmap_review_routine":
			var payload roadmapRoutinePayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode roadmap review routine %s/%d: %w", envelope.RecordID, envelope.RecordRevision, err)
			}
			value, err := decodeRoadmapRoutine(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.roadmap.Routines[roadmapRecordKey(envelope.RecordID, envelope.RecordRevision)] = value
		case "roadmap_sync":
			var payload roadmapSyncPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode roadmap sync %s: %w", envelope.RecordID, err)
			}
			value, err := decodeRoadmapSync(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.roadmap.Syncs[envelope.RecordID] = value
		case "roadmap_release":
			var payload releasePayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode roadmap release %s/%d: %w", envelope.RecordID, envelope.RecordRevision, err)
			}
			value, err := decodeRelease(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.releases[roadmapRecordKey(envelope.RecordID, envelope.RecordRevision)] = value
		case "work_item":
			var payload workItemPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode work item %s: %w", envelope.RecordID, err)
			}
			value, err := decodeWorkItem(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.reviews.Items[envelope.RecordID] = value
		case "work_item_disposition":
			var payload workItemDispositionPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode work item disposition %s: %w", envelope.RecordID, err)
			}
			value, err := decodeWorkItemDisposition(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.reviews.Dispositions[envelope.RecordID] = value
		case "work_item_link":
			var payload workItemLinkPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode work item link %s: %w", envelope.RecordID, err)
			}
			value, err := decodeWorkItemLink(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.reviews.Links[envelope.RecordID] = value
		case "track_plan":
			var payload trackPlanPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode track plan %s/%d: %w", envelope.RecordID, envelope.RecordRevision, err)
			}
			value, err := decodeTrackPlan(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.trackPlans.Plans[roadmapRecordKey(envelope.RecordID, envelope.RecordRevision)] = value
		case "checkpoint":
			var payload checkpointPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode checkpoint %s: %w", envelope.RecordID, err)
			}
			value, err := decodeCheckpoint(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.checkpoints[envelope.RecordID] = value
		case "checkpoint_action":
			var payload checkpointActionPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode checkpoint action %s: %w", envelope.RecordID, err)
			}
			value, err := decodeCheckpointAction(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.actions[envelope.RecordID] = value
		case "checkpoint_action_transition":
			var payload checkpointTransitionPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode checkpoint transition %s: %w", envelope.RecordID, err)
			}
			value, err := decodeCheckpointTransition(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.transitions[envelope.RecordID] = value
		case "actor_invocation":
			var payload invocationPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode actor invocation %s: %w", envelope.RecordID, err)
			}
			invocation, err := decodeInvocation(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.invocations[envelope.RecordID] = invocation
		case "work_narrative":
			var payload workNarrativePayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode work narrative %s: %w", envelope.RecordID, err)
			}
			value, err := decodeWorkNarrative(envelope, payload)
			if err != nil {
				return decoded, err
			}
			decoded.workNarratives[envelope.RecordID] = value
		case "work_narrative_supersession":
			var payload supersessionPayload
			if err := strictDecode(envelope.Payload, &payload); err != nil {
				return decoded, fmt.Errorf("decode work narrative supersession %s: %w", envelope.RecordID, err)
			}
			if payload.SuccessorID != envelope.RecordID || payload.SupersedesID == "" || payload.SuccessorID == payload.SupersedesID || !sameReferences(envelope.Refs, []Reference{{Kind: "narrative", ID: payload.SuccessorID}, {Kind: "narrative", ID: payload.SupersedesID}}) {
				return decoded, fmt.Errorf("invalid work narrative supersession %s", envelope.RecordID)
			}
			decoded.workEdges[envelope.RecordID] = decodedSupersession{payload: payload, authority: envelope.Authority, recordedAt: envelope.RecordedAt}
		default:
			// Supported by the portable format but intentionally not by this importer.
		}
	}
	for id, edge := range decoded.workEdges {
		narrative, exists := decoded.workNarratives[id]
		if !exists {
			return decoded, fmt.Errorf("work narrative supersession %s has no successor narrative in the bundle", id)
		}
		if narrative.RecordedAt.UTC().Format(time.RFC3339Nano) != edge.recordedAt || edge.authority != "mixed" {
			return decoded, fmt.Errorf("work narrative supersession %s metadata does not match its narrative", id)
		}
		narrative.SupersedesID = edge.payload.SupersedesID
		decoded.workNarratives[id] = narrative
	}
	return decoded, nil
}

func decodeInvocation(envelope Envelope, payload invocationPayload) (attribution.Invocation, error) {
	if payload.ActorType == "" || payload.IdentitySource == "" || payload.OperationalSchemaVersion != attribution.InvocationSchemaVersion || envelope.Authority != payload.IdentitySource {
		return attribution.Invocation{}, fmt.Errorf("actor invocation %s has invalid provenance or schema", envelope.RecordID)
	}
	if !oneOfValue(payload.ActorType, "unknown", "human", "agent", "system", "tool") {
		return attribution.Invocation{}, fmt.Errorf("actor invocation %s has unsupported actor type", envelope.RecordID)
	}
	for _, value := range []string{payload.SessionID, payload.ParentInvocationID, payload.ActorType, payload.Client, payload.ClientVersion, payload.Provider, payload.Model, payload.IdentitySource} {
		if !validBoundedText(value, 16<<10) {
			return attribution.Invocation{}, fmt.Errorf("actor invocation %s has an oversized field", envelope.RecordID)
		}
	}
	occurred, err := parsePortableTime(payload.OccurredAt)
	if err != nil || payload.OccurredAt != envelope.RecordedAt {
		return attribution.Invocation{}, fmt.Errorf("actor invocation %s has invalid occurred time", envelope.RecordID)
	}
	received, err := parsePortableTime(payload.ReceivedAt)
	if err != nil {
		return attribution.Invocation{}, fmt.Errorf("actor invocation %s has invalid received time", envelope.RecordID)
	}
	if err := validatePortableEffort(payload.EffortRequested); err != nil {
		return attribution.Invocation{}, fmt.Errorf("actor invocation %s requested effort: %w", envelope.RecordID, err)
	}
	if err := validatePortableEffort(payload.EffortEffective); err != nil {
		return attribution.Invocation{}, fmt.Errorf("actor invocation %s effective effort: %w", envelope.RecordID, err)
	}
	if !sameReferences(envelope.Refs, invocationReferences(payload.SessionID, payload.ParentInvocationID)) {
		return attribution.Invocation{}, fmt.Errorf("actor invocation %s references do not match payload", envelope.RecordID)
	}
	return attribution.Invocation{ID: envelope.RecordID, SessionID: payload.SessionID, ParentInvocationID: payload.ParentInvocationID, ActorType: payload.ActorType, Client: payload.Client, ClientVersion: payload.ClientVersion, Provider: payload.Provider, Model: payload.Model, EffortRequested: payload.EffortRequested, EffortEffective: payload.EffortEffective, IdentitySource: payload.IdentitySource, OccurredAt: occurred, ReceivedAt: received, SchemaVersion: payload.OperationalSchemaVersion}, nil
}

func decodeWorkNarrative(envelope Envelope, payload workNarrativePayload) (worknarrative.Record, error) {
	if envelope.Authority != "mixed" || payload.Title == "" || payload.Creator == "" || payload.OperationalSchemaVersion != worknarrative.SchemaVersion || !oneOfValue(payload.Trigger, "meaningful_checkpoint", "accepted_decision", "completed_roadmap_slice", "commit", "manual") {
		return worknarrative.Record{}, fmt.Errorf("work narrative %s has invalid provenance or required fields", envelope.RecordID)
	}
	if len(payload.Claims) == 0 || len(payload.Claims) > worknarrative.MaxClaims || len(payload.Anchors) > worknarrative.MaxAnchors {
		return worknarrative.Record{}, fmt.Errorf("work narrative %s has invalid claim or anchor counts", envelope.RecordID)
	}
	normalizedSource, normalizedClaims, err := worknarrative.NormalizeResearch(payload.Source, payload.Claims)
	if err != nil || !reflect.DeepEqual(normalizedSource, payload.Source) || !reflect.DeepEqual(normalizedClaims, payload.Claims) {
		return worknarrative.Record{}, fmt.Errorf("work narrative %s has invalid or noncanonical research metadata: %v", envelope.RecordID, err)
	}
	for _, claim := range payload.Claims {
		if !oneOfValue(claim.Kind, "intent", "outcome", "interface", "constraint", "alternative", "verification", "decision", "next_action", "limitation", "note") || len(claim.Label) > worknarrative.MaxClaimLabelBytes || !oneOfValue(claim.Authority, "machine_observed", "caller_asserted", "user_reported") || !validBoundedText(claim.Text, worknarrative.MaxRecordBytes) || claim.Text == "" {
			return worknarrative.Record{}, fmt.Errorf("work narrative %s has an invalid claim", envelope.RecordID)
		}
	}
	for _, anchor := range payload.Anchors {
		if !oneOfValue(anchor.Kind, "checkpoint", "task", "roadmap_node", "commit", "file", "region", "invocation", "verification", "portable_evidence") || !oneOfValue(anchor.Authority, "machine_observed", "caller_asserted", "user_reported") || !validBoundedText(anchor.Target, worknarrative.MaxRecordBytes) || anchor.Target == "" || anchor.Drift != "" {
			return worknarrative.Record{}, fmt.Errorf("work narrative %s has an invalid anchor", envelope.RecordID)
		}
		if anchor.Kind == "file" || anchor.Kind == "region" {
			if filepath.IsAbs(anchor.Target) || anchor.Target == ".." || strings.HasPrefix(anchor.Target, "../") || filepath.ToSlash(filepath.Clean(anchor.Target)) != anchor.Target || !portableHashPattern.MatchString(anchor.ContentSHA256) {
				return worknarrative.Record{}, fmt.Errorf("work narrative %s has an invalid source anchor", envelope.RecordID)
			}
		} else if anchor.ContentSHA256 != "" && !portableHashPattern.MatchString(anchor.ContentSHA256) {
			return worknarrative.Record{}, fmt.Errorf("work narrative %s has an invalid anchor hash", envelope.RecordID)
		}
	}
	if !sameReferences(envelope.Refs, workNarrativeReferences(payload.Anchors, payload.CreatorInvocationID, payload.Claims)) {
		return worknarrative.Record{}, fmt.Errorf("work narrative %s references do not match payload", envelope.RecordID)
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	if err != nil {
		return worknarrative.Record{}, err
	}
	from, err := parseOptionalPortableTime(payload.OccurredFrom)
	if err != nil {
		return worknarrative.Record{}, fmt.Errorf("work narrative %s occurred_from: %w", envelope.RecordID, err)
	}
	to, err := parseOptionalPortableTime(payload.OccurredTo)
	if err != nil {
		return worknarrative.Record{}, fmt.Errorf("work narrative %s occurred_to: %w", envelope.RecordID, err)
	}
	if from != nil && to != nil && from.After(*to) {
		return worknarrative.Record{}, fmt.Errorf("work narrative %s has reversed occurrence boundary", envelope.RecordID)
	}
	return worknarrative.Record{Source: payload.Source, ID: envelope.RecordID, RecordType: worknarrative.RecordType, Title: payload.Title, Trigger: payload.Trigger, OccurredFrom: from, OccurredTo: to, Claims: append([]worknarrative.Claim{}, payload.Claims...), Anchors: append([]worknarrative.Anchor{}, payload.Anchors...), Creator: payload.Creator, CreatorInvocationID: payload.CreatorInvocationID, State: "current", RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}, nil
}

func validateApplyDependencies(ctx context.Context, tx *sql.Tx, projectID string, decoded decodedOperationalBundle, appendKeys map[string]struct{}) error {
	if err := validateResourceDependencies(ctx, tx, projectID, decoded.resources, appendKeys); err != nil {
		return err
	}
	if err := validateLedgerDependencies(ctx, tx, projectID, decoded.ledger, appendKeys); err != nil {
		return err
	}
	for id, value := range decoded.checkpoints {
		if _, append := appendKeys[identityKey("checkpoint", id, 0)]; !append {
			continue
		}
		local := value
		local.ProjectID = projectID
		if _, err := encodeImportedCheckpoint(local); err != nil {
			return fmt.Errorf("checkpoint %s cannot fit local storage: %w", id, err)
		}
	}
	for id, value := range decoded.actions {
		if _, append := appendKeys[identityKey("checkpoint_action", id, 0)]; !append {
			continue
		}
		if _, bundled := decoded.checkpoints[value.SourceCheckpointID]; !bundled {
			exists, err := rowExists(ctx, tx, `SELECT 1 FROM checkpoints WHERE id=? AND project_id=?`, value.SourceCheckpointID, projectID)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("checkpoint action %s references a missing source checkpoint", id)
			}
		}
	}
	for key, value := range decoded.contexts {
		if _, append := appendKeys[key]; !append {
			continue
		}
		if _, bundled := decoded.checkpoints[value.CheckpointID]; !bundled {
			exists, err := rowExists(ctx, tx, `SELECT 1 FROM checkpoints WHERE id=? AND project_id=?`, value.CheckpointID, projectID)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("checkpoint context %s references a missing checkpoint", value.CheckpointID)
			}
		}
	}
	for key, value := range decoded.branches {
		if _, append := appendKeys[key]; !append {
			continue
		}
		if _, bundled := decoded.checkpoints[value.CheckpointID]; !bundled {
			exists, err := rowExists(ctx, tx, `SELECT 1 FROM checkpoints WHERE id=? AND project_id=?`, value.CheckpointID, projectID)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("checkpoint branches %s reference a missing checkpoint", value.CheckpointID)
			}
		}
	}
	for id, value := range decoded.transitions {
		if _, append := appendKeys[identityKey("checkpoint_action_transition", id, 0)]; !append {
			continue
		}
		action, bundled := decoded.actions[value.ActionID]
		if !bundled {
			err := tx.QueryRowContext(ctx, `SELECT source_checkpoint_id FROM checkpoint_actions WHERE id=? AND project_id=?`, value.ActionID, projectID).Scan(&action.SourceCheckpointID)
			if err == sql.ErrNoRows {
				return fmt.Errorf("checkpoint action transition %s references a missing action", id)
			}
			if err != nil {
				return err
			}
		}
		if value.Status == "open" && value.CheckpointID != action.SourceCheckpointID {
			return fmt.Errorf("checkpoint action transition %s open scope does not match its source checkpoint", id)
		}
		if value.CheckpointID != "" {
			if _, bundled := decoded.checkpoints[value.CheckpointID]; !bundled {
				exists, err := rowExists(ctx, tx, `SELECT 1 FROM checkpoints WHERE id=? AND project_id=?`, value.CheckpointID, projectID)
				if err != nil {
					return err
				}
				if !exists {
					return fmt.Errorf("checkpoint action transition %s references a missing checkpoint", id)
				}
			}
		}
	}
	for id, value := range decoded.roadmap.Bindings {
		if _, append := appendKeys[identityKey("roadmap_binding", id, 0)]; !append {
			continue
		}
		if _, bundled := decoded.roadmap.Nodes[roadmapRecordKey(value.NodeID, value.NodeRevision)]; !bundled {
			exists, err := rowExists(ctx, tx, `SELECT 1 FROM roadmap_nodes WHERE project_id=? AND node_id=? AND revision=?`, projectID, value.NodeID, value.NodeRevision)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("roadmap binding %s references a missing node revision", id)
			}
		}
	}
	for id, value := range decoded.roadmap.Links {
		if _, append := appendKeys[identityKey("roadmap_link", id, 0)]; !append {
			continue
		}
		if _, bundled := decoded.roadmap.Nodes[roadmapRecordKey(value.NodeID, value.NodeRevision)]; !bundled {
			exists, err := rowExists(ctx, tx, `SELECT 1 FROM roadmap_nodes WHERE project_id=? AND node_id=? AND revision=?`, projectID, value.NodeID, value.NodeRevision)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("roadmap link %s references a missing source node revision", id)
			}
		}
		if !bundledRoadmapNode(decoded.roadmap.Nodes, value.TargetNodeID) {
			exists, err := rowExists(ctx, tx, `SELECT 1 FROM roadmap_nodes WHERE project_id=? AND node_id=?`, projectID, value.TargetNodeID)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("roadmap link %s references a missing target node", id)
			}
		}
	}
	for id, value := range decoded.roadmap.Syncs {
		if _, append := appendKeys[identityKey("roadmap_sync", id, 0)]; !append {
			continue
		}
		for _, nodeID := range value.ReviewedNodes {
			if bundledRoadmapNode(decoded.roadmap.Nodes, nodeID) {
				continue
			}
			exists, err := rowExists(ctx, tx, `SELECT 1 FROM roadmap_nodes WHERE project_id=? AND node_id=?`, projectID, nodeID)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("roadmap sync %s references a missing reviewed node", id)
			}
		}
		if value.InvocationID != "" {
			if _, bundled := decoded.invocations[value.InvocationID]; !bundled {
				exists, err := rowExists(ctx, tx, `SELECT 1 FROM actor_invocations WHERE id=? AND project_id=?`, value.InvocationID, projectID)
				if err != nil {
					return err
				}
				if !exists {
					return fmt.Errorf("roadmap sync %s references a missing invocation", id)
				}
			}
		}
	}
	for _, value := range decoded.releases {
		if _, appendRecord := appendKeys[identityKey("roadmap_release", value.ID, value.Revision)]; !appendRecord {
			continue
		}
		for _, entry := range value.Entries {
			if bundled, ok := decoded.roadmap.Nodes[roadmapRecordKey(entry.NodeID, entry.NodeRevision)]; ok {
				if bundled.Status != "completed" {
					return fmt.Errorf("roadmap release %s/%d references a non-completed node revision", value.ID, value.Revision)
				}
			} else {
				var status string
				err := tx.QueryRowContext(ctx, `SELECT status FROM roadmap_nodes WHERE project_id=? AND node_id=? AND revision=?`, projectID, entry.NodeID, entry.NodeRevision).Scan(&status)
				if err == sql.ErrNoRows {
					return fmt.Errorf("roadmap release %s/%d references a missing node revision", value.ID, value.Revision)
				}
				if err != nil {
					return err
				}
				if status != "completed" {
					return fmt.Errorf("roadmap release %s/%d references a non-completed node revision", value.ID, value.Revision)
				}
			}
			for _, narrativeID := range entry.NarrativeIDs {
				if _, bundled := decoded.workNarratives[narrativeID]; bundled {
					continue
				}
				exists, err := rowExists(ctx, tx, `SELECT 1 FROM work_narratives WHERE project_id=? AND id=?`, projectID, narrativeID)
				if err != nil {
					return err
				}
				if !exists {
					return fmt.Errorf("roadmap release %s/%d references missing narrative %s", value.ID, value.Revision, narrativeID)
				}
			}
		}
		if value.Status == "published" {
			previousStatus, previousHash, previousRevision := "", "", 0
			for _, candidate := range decoded.releases {
				if candidate.ID == value.ID && candidate.Revision < value.Revision && candidate.Revision > previousRevision {
					previousStatus, previousHash, previousRevision = candidate.Status, candidate.ContentSHA256, candidate.Revision
				}
			}
			var localRevision int
			var localStatus, localHash string
			err := tx.QueryRowContext(ctx, `SELECT revision,status,content_sha256 FROM roadmap_releases WHERE project_id=? AND release_id=? AND revision<? ORDER BY revision DESC LIMIT 1`, projectID, value.ID, value.Revision).Scan(&localRevision, &localStatus, &localHash)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == nil && localRevision > previousRevision {
				previousStatus, previousHash, previousRevision = localStatus, localHash, localRevision
			}
			if previousStatus != "reviewed" || previousHash != value.ContentSHA256 {
				return fmt.Errorf("published roadmap release %s/%d does not match its latest reviewed revision", value.ID, value.Revision)
			}
		}
	}
	workItemExists := func(id string) (bool, error) {
		if _, bundled := decoded.reviews.Items[id]; bundled {
			return true, nil
		}
		return rowExists(ctx, tx, `SELECT 1 FROM work_items WHERE project_id=? AND id=?`, projectID, id)
	}
	for id, value := range decoded.reviews.Dispositions {
		if _, append := appendKeys[identityKey("work_item_disposition", id, 0)]; !append {
			continue
		}
		if exists, err := workItemExists(value.ItemID); err != nil || !exists {
			if err != nil {
				return err
			}
			return fmt.Errorf("work item disposition %s references a missing work item", id)
		}
	}
	for id, value := range decoded.reviews.Links {
		if _, append := appendKeys[identityKey("work_item_link", id, 0)]; !append {
			continue
		}
		if exists, err := workItemExists(value.ItemID); err != nil || !exists {
			if err != nil {
				return err
			}
			return fmt.Errorf("work item link %s references a missing work item", id)
		}
	}
	for _, value := range decoded.trackPlans.Plans {
		if _, appendRecord := appendKeys[identityKey("track_plan", value.ID, value.Revision)]; !appendRecord {
			continue
		}
		if value.Revision > 1 {
			if _, bundled := decoded.trackPlans.Plans[roadmapRecordKey(value.ID, value.Revision-1)]; !bundled {
				exists, err := rowExists(ctx, tx, `SELECT 1 FROM track_plan_revisions WHERE project_id=? AND id=? AND revision=?`, projectID, value.ID, value.Revision-1)
				if err != nil {
					return err
				}
				if !exists {
					return fmt.Errorf("track plan %s/%d is missing its prior revision", value.ID, value.Revision)
				}
			}
		}
		if value.Change != nil {
			var base *trackplan.Plan
			if value.Change.ComparisonRevision > 0 {
				if v, ok := decoded.trackPlans.Plans[roadmapRecordKey(value.ID, value.Change.ComparisonRevision)]; ok {
					base = &v
				} else {
					v, err := loadTrackPlan(ctx, tx, projectID, value.ID, value.Change.ComparisonRevision)
					if err != nil {
						return err
					}
					base = &v
				}
			}
			if err := trackplan.ValidateChange(value, base); err != nil {
				return err
			}
		}
		for _, itemID := range value.WorkItemIDs {
			exists, err := workItemExists(itemID)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("track plan %s/%d references missing work item %s", value.ID, value.Revision, itemID)
			}
		}
	}
	for id, invocation := range decoded.invocations {
		if _, append := appendKeys[identityKey("actor_invocation", id, 0)]; !append || invocation.ParentInvocationID == "" {
			continue
		}
		if _, bundled := decoded.invocations[invocation.ParentInvocationID]; bundled {
			continue
		}
		exists, err := rowExists(ctx, tx, `SELECT 1 FROM actor_invocations WHERE id=? AND project_id=?`, invocation.ParentInvocationID, projectID)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("actor invocation %s references a missing parent", id)
		}
	}
	for id := range decoded.workEdges {
		if _, exists := decoded.workNarratives[id]; !exists {
			return fmt.Errorf("work narrative supersession %s has no successor", id)
		}
		if _, edgeAppend := appendKeys[identityKey("work_narrative_supersession", id, 0)]; edgeAppend {
			if _, narrativeAppend := appendKeys[identityKey("work_narrative", id, 0)]; !narrativeAppend {
				return fmt.Errorf("work narrative supersession %s cannot be appended to an existing immutable narrative", id)
			}
		}
	}
	for id, narrative := range decoded.workNarratives {
		for _, claim := range narrative.Claims {
			for _, citation := range claim.Citations {
				target := strings.TrimPrefix(citation.Source, "narrative:")
				if target == id {
					return fmt.Errorf("narrative %s cites itself", id)
				}
				if source, ok := decoded.workNarratives[target]; ok {
					if source.Source == nil {
						return fmt.Errorf("citation target %s is not a source observation", target)
					}
				} else if err := worknarrative.ValidateCitationTargets(ctx, tx, projectID, []worknarrative.Claim{{Citations: []worknarrative.Citation{citation}}}); err != nil {
					return err
				}
			}
		}
		if _, appendRecord := appendKeys[identityKey("work_narrative", id, 0)]; !appendRecord {
			continue
		}
		if narrative.CreatorInvocationID != "" {
			if _, bundled := decoded.invocations[narrative.CreatorInvocationID]; !bundled {
				exists, err := rowExists(ctx, tx, `SELECT 1 FROM actor_invocations WHERE id=? AND project_id=?`, narrative.CreatorInvocationID, projectID)
				if err != nil {
					return err
				}
				if !exists {
					return fmt.Errorf("work narrative %s references a missing creator invocation", id)
				}
			}
		}
		if narrative.SupersedesID != "" {
			if _, bundled := decoded.workNarratives[narrative.SupersedesID]; !bundled {
				exists, err := rowExists(ctx, tx, `SELECT 1 FROM work_narratives WHERE id=? AND project_id=?`, narrative.SupersedesID, projectID)
				if err != nil {
					return err
				}
				if !exists {
					return fmt.Errorf("work narrative %s references missing superseded narrative", id)
				}
			}
		}
		local := narrative
		local.ProjectID = projectID
		if _, _, err := encodeImportedWorkNarrative(local); err != nil {
			return fmt.Errorf("work narrative %s cannot fit local storage: %w", id, err)
		}
	}
	return nil
}

func bundledRoadmapNode(values map[string]roadmap.Node, id string) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func appendRoadmapGraph(ctx context.Context, tx *sql.Tx, projectID string, values portableRoadmapRecords, appendKeys map[string]struct{}) error {
	nodes := make([]roadmap.Node, 0, len(values.Nodes))
	for _, value := range values.Nodes {
		nodes = append(nodes, value)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].ID != nodes[j].ID {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].Revision < nodes[j].Revision
	})
	for _, value := range nodes {
		if _, append := appendKeys[identityKey("roadmap_node", value.ID, value.Revision)]; !append {
			continue
		}
		evidence, marshalErr := json.Marshal(value.EvidenceRefs)
		if marshalErr != nil {
			return marshalErr
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,kind,target_date,target_release,target_horizon,evidence_refs_json,source_path,source_ref,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,NULLIF(?,''),?,?,?)`, projectID, value.ID, value.Revision, value.Title, value.Status, value.Priority, value.Kind, value.TargetDate, value.TargetRelease, value.TargetHorizon, string(evidence), value.SourcePath, value.SourceRef, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion)
		if err != nil {
			return fmt.Errorf("append roadmap node %s/%d: %w", value.ID, value.Revision, err)
		}
	}
	routines := make([]roadmap.ReviewRoutine, 0, len(values.Routines))
	for _, value := range values.Routines {
		routines = append(routines, value)
	}
	sort.Slice(routines, func(i, j int) bool {
		if routines[i].ID != routines[j].ID {
			return routines[i].ID < routines[j].ID
		}
		return routines[i].Revision < routines[j].Revision
	})
	for _, value := range routines {
		if _, append := appendKeys[identityKey("roadmap_review_routine", value.ID, value.Revision)]; !append {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO roadmap_review_routines(project_id,routine_id,revision,title,status,commit_threshold,elapsed_seconds_threshold,changed_files_threshold,source,source_ref,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?,?)`, projectID, value.ID, value.Revision, value.Title, value.Status, value.CommitThreshold, value.ElapsedSecondsThreshold, value.ChangedFilesThreshold, value.Source, value.SourceRef, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion)
		if err != nil {
			return fmt.Errorf("append roadmap review routine %s/%d: %w", value.ID, value.Revision, err)
		}
	}
	for _, id := range sortedRoadmapKeys(values.Bindings) {
		value := values.Bindings[id]
		if _, append := appendKeys[identityKey("roadmap_binding", id, 0)]; !append {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO roadmap_file_bindings(id,project_id,node_id,node_revision,path,path_kind,relationship,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?)`, value.ID, projectID, value.NodeID, value.NodeRevision, value.Path, value.PathKind, value.Relationship, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion)
		if err != nil {
			return fmt.Errorf("append roadmap binding %s: %w", id, err)
		}
	}
	for _, id := range sortedRoadmapKeys(values.Links) {
		value := values.Links[id]
		if _, append := appendKeys[identityKey("roadmap_link", id, 0)]; !append {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO roadmap_node_links(id,project_id,node_id,node_revision,target_node_id,relationship,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?)`, value.ID, projectID, value.NodeID, value.NodeRevision, value.TargetNodeID, value.Relationship, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion)
		if err != nil {
			return fmt.Errorf("append roadmap link %s: %w", id, err)
		}
	}
	return nil
}

func appendRoadmapSyncs(ctx context.Context, tx *sql.Tx, projectID string, values map[string]roadmap.SyncRun, appendKeys map[string]struct{}) error {
	for _, id := range sortedRoadmapKeys(values) {
		value := values[id]
		if _, append := appendKeys[identityKey("roadmap_sync", id, 0)]; !append {
			continue
		}
		nodesJSON, err := json.Marshal(nonNilStrings(value.ReviewedNodes))
		if err != nil {
			return err
		}
		filesJSON, err := json.Marshal(nonNilStrings(value.ReviewedFiles))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO roadmap_sync_runs(id,project_id,git_commit,client,model,invocation_id,reviewed_nodes_json,reviewed_files_json,authority,git_authority,completed_at,schema_version) VALUES(?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,?,?,?,?)`, value.ID, projectID, value.GitCommit, value.Client, value.Model, value.InvocationID, string(nodesJSON), string(filesJSON), value.Authority, value.GitAuthority, value.CompletedAt.Format(time.RFC3339Nano), value.SchemaVersion)
		if err != nil {
			return fmt.Errorf("append roadmap sync %s: %w", id, err)
		}
	}
	return nil
}

func appendReviewRecords(ctx context.Context, tx *sql.Tx, projectID string, values portableReviewRecords, appendKeys map[string]struct{}) error {
	items := make([]workitem.Item, 0, len(values.Items))
	for _, value := range values.Items {
		items = append(items, value)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].RecordedAt.Equal(items[j].RecordedAt) {
			return items[i].RecordedAt.Before(items[j].RecordedAt)
		}
		return items[i].ID < items[j].ID
	})
	for _, value := range items {
		if _, append := appendKeys[identityKey("work_item", value.ID, 0)]; !append {
			continue
		}
		value.ProjectID = projectID
		if err := workitem.Insert(ctx, tx, value); err != nil {
			return err
		}
	}
	links := make([]workitem.Link, 0, len(values.Links))
	for _, value := range values.Links {
		links = append(links, value)
	}
	sort.Slice(links, func(i, j int) bool {
		if !links[i].RecordedAt.Equal(links[j].RecordedAt) {
			return links[i].RecordedAt.Before(links[j].RecordedAt)
		}
		return links[i].ID < links[j].ID
	})
	for _, value := range links {
		if _, append := appendKeys[identityKey("work_item_link", value.ID, 0)]; !append {
			continue
		}
		value.ProjectID = projectID
		if err := workitem.InsertLink(ctx, tx, value); err != nil {
			return err
		}
	}
	dispositions := make([]workitem.Disposition, 0, len(values.Dispositions))
	for _, value := range values.Dispositions {
		dispositions = append(dispositions, value)
	}
	sort.Slice(dispositions, func(i, j int) bool {
		if !dispositions[i].RecordedAt.Equal(dispositions[j].RecordedAt) {
			return dispositions[i].RecordedAt.Before(dispositions[j].RecordedAt)
		}
		return dispositions[i].ID < dispositions[j].ID
	})
	for _, value := range dispositions {
		if _, append := appendKeys[identityKey("work_item_disposition", value.ID, 0)]; !append {
			continue
		}
		value.ProjectID = projectID
		value.TargetRefs = nonNilStrings(value.TargetRefs)
		if err := workitem.InsertDisposition(ctx, tx, value); err != nil {
			return err
		}
	}
	return nil
}

func appendTrackPlanRecords(ctx context.Context, tx *sql.Tx, projectID string, values portableTrackPlanRecords, appendKeys map[string]struct{}) error {
	plans := make([]trackplan.Plan, 0, len(values.Plans))
	for _, value := range values.Plans {
		plans = append(plans, value)
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].ID != plans[j].ID {
			return plans[i].ID < plans[j].ID
		}
		return plans[i].Revision < plans[j].Revision
	})
	for _, value := range plans {
		if _, appendRecord := appendKeys[identityKey("track_plan", value.ID, value.Revision)]; !appendRecord {
			continue
		}
		if value.Change != nil {
			var base *trackplan.Plan
			if value.Change.ComparisonRevision > 0 {
				v, err := loadTrackPlan(ctx, tx, projectID, value.ID, value.Change.ComparisonRevision)
				if err != nil {
					return err
				}
				base = &v
			}
			if err := trackplan.ValidateChange(value, base); err != nil {
				return err
			}
		}
		changeJSON, _ := json.Marshal(value.Change)
		_, err := tx.ExecContext(ctx, `INSERT INTO track_plan_revisions(project_id,id,revision,roadmap_node_id,task_id,title,summary,status,creator,model,authority,recorded_at,schema_version,change_json) VALUES(?,?,?,?,NULLIF(?,''),?,?,?,?,NULLIF(?,''),?,?,?,?)`, projectID, value.ID, value.Revision, value.RoadmapNodeID, value.TaskID, value.Title, value.Summary, value.Status, value.Creator, value.Model, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion, string(changeJSON))
		if err != nil {
			return fmt.Errorf("append track plan %s/%d: %w", value.ID, value.Revision, err)
		}
		for _, item := range value.Items {
			evidence, err := json.Marshal(nonNilStrings(item.EvidenceRefs))
			if err != nil {
				return err
			}
			workItemRefs, err := json.Marshal(nonNilStrings(item.WorkItemIDs))
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO track_plan_items(project_id,plan_id,plan_revision,id,position,title,detail,status,parent_id,evidence_refs_json,work_item_refs_json,progress_note,acceptance_criteria) VALUES(?,?,?,?,?,?,?,?,NULLIF(?,''),?,?,?,?)`, projectID, value.ID, value.Revision, item.ID, item.Position, item.Title, item.Detail, item.Status, item.ParentID, string(evidence), string(workItemRefs), item.ProgressNote, item.AcceptanceCriteria)
			if err != nil {
				return fmt.Errorf("append track plan item %s/%d/%s: %w", value.ID, value.Revision, item.ID, err)
			}
		}
		for _, item := range value.Items {
			for _, dependency := range item.DependsOn {
				if _, err := tx.ExecContext(ctx, `INSERT INTO track_plan_item_dependencies(project_id,plan_id,plan_revision,item_id,depends_on_item_id) VALUES(?,?,?,?,?)`, projectID, value.ID, value.Revision, item.ID, dependency); err != nil {
					return fmt.Errorf("append track plan dependency: %w", err)
				}
			}
		}
		for _, itemID := range value.WorkItemIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO track_plan_work_items(project_id,plan_id,plan_revision,item_id) VALUES(?,?,?,?)`, projectID, value.ID, value.Revision, itemID); err != nil {
				return fmt.Errorf("append track plan work item: %w", err)
			}
		}
	}
	return nil
}

func appendCheckpoints(ctx context.Context, tx *sql.Tx, projectID string, decoded decodedOperationalBundle, appendKeys map[string]struct{}) error {
	for _, id := range sortedCheckpointKeys(decoded.checkpoints) {
		value := decoded.checkpoints[id]
		if _, append := appendKeys[identityKey("checkpoint", id, 0)]; !append {
			continue
		}
		value.ProjectID = projectID
		payload, err := encodeImportedCheckpoint(value)
		if err != nil {
			return fmt.Errorf("prepare imported checkpoint %s: %w", id, err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO checkpoints(id,project_id,session_id,task_id,task_title,status,summary,next_action,payload_json,created_at) VALUES(?,?,NULLIF(?,''),NULLIF(?,''),?,?,?,?,?,?)`, value.ID, projectID, value.SessionID, value.TaskID, value.Task, value.Status, value.Summary, value.NextAction, payload, value.CreatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("append checkpoint %s: %w", id, err)
		}
	}
	for _, id := range sortedCheckpointKeys(decoded.actions) {
		value := decoded.actions[id]
		if _, append := appendKeys[identityKey("checkpoint_action", id, 0)]; !append {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoint_actions(id,project_id,source_checkpoint_id,description,created_at) VALUES(?,?,?,?,?)`, value.ID, projectID, value.SourceCheckpointID, value.Description, value.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("append checkpoint action %s: %w", id, err)
		}
	}
	for _, id := range sortedCheckpointKeys(decoded.transitions) {
		value := decoded.transitions[id]
		if _, append := appendKeys[identityKey("checkpoint_action_transition", id, 0)]; !append {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoint_action_transitions(id,action_id,checkpoint_id,status,reason,created_at) VALUES(?,?,NULLIF(?,''),?,NULLIF(?,''),?)`, value.ID, value.ActionID, value.CheckpointID, value.Status, value.Reason, value.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("append checkpoint action transition %s: %w", id, err)
		}
	}
	return nil
}

func appendInvocations(ctx context.Context, tx *sql.Tx, projectID string, values map[string]attribution.Invocation, appendKeys map[string]struct{}) error {
	pending := make(map[string]attribution.Invocation)
	for id, value := range values {
		if _, append := appendKeys[identityKey("actor_invocation", id, 0)]; append {
			pending[id] = value
		}
	}
	ordered, err := dependencyOrder(pending, func(value attribution.Invocation) string { return value.ParentInvocationID })
	if err != nil {
		return fmt.Errorf("actor invocation parent references: %w", err)
	}
	for _, id := range ordered {
		value := pending[id]
		requested, effective := effortColumns(value.EffortRequested), effortColumns(value.EffortEffective)
		_, err = tx.ExecContext(ctx, `INSERT INTO actor_invocations(id,project_id,session_id,parent_invocation_id,actor_type,client,client_version,provider,model,identity_source,occurred_at,received_at,schema_version,effort_requested_raw,effort_requested_normalized,effort_requested_source,effort_requested_authority,effort_effective_raw,effort_effective_normalized,effort_effective_source,effort_effective_authority) VALUES(?,?,NULLIF(?,''),NULLIF(?,''),?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''))`, value.ID, projectID, value.SessionID, value.ParentInvocationID, value.ActorType, value.Client, value.ClientVersion, value.Provider, value.Model, value.IdentitySource, value.OccurredAt.Format(time.RFC3339Nano), value.ReceivedAt.Format(time.RFC3339Nano), value.SchemaVersion, requested[0], requested[1], requested[2], requested[3], effective[0], effective[1], effective[2], effective[3])
		if err != nil {
			return fmt.Errorf("append actor invocation %s: %w", id, err)
		}
	}
	return nil
}

func appendWorkNarratives(ctx context.Context, tx *sql.Tx, projectID string, decoded decodedOperationalBundle, appendKeys map[string]struct{}) error {
	pending := make(map[string]worknarrative.Record)
	for id, value := range decoded.workNarratives {
		if _, appendRecord := appendKeys[identityKey("work_narrative", id, 0)]; appendRecord {
			value.ProjectID = projectID
			pending[id] = value
		}
	}
	ordered, err := dependencyOrder(pending, func(value worknarrative.Record) string { return value.SupersedesID })
	if err != nil {
		return fmt.Errorf("work narrative supersession references: %w", err)
	}
	for _, id := range ordered {
		record := pending[id]
		payload, semanticHash, err := encodeImportedWorkNarrative(record)
		if err != nil {
			return fmt.Errorf("prepare imported work narrative %s: %w", id, err)
		}
		record.SemanticSHA256 = semanticHash
		_, err = tx.ExecContext(ctx, `INSERT INTO work_narratives(id,project_id,title,trigger_kind,occurred_from,occurred_to,creator,creator_invocation_id,semantic_sha256,supersedes_id,recorded_at,schema_version,payload_json) VALUES(?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,NULLIF(?,''),?,NULLIF(?,''),?,?,?)`, record.ID, projectID, record.Title, record.Trigger, optionalTimeString(record.OccurredFrom), optionalTimeString(record.OccurredTo), record.Creator, record.CreatorInvocationID, record.SemanticSHA256, record.SupersedesID, record.RecordedAt.Format(time.RFC3339Nano), record.SchemaVersion, payload)
		if err != nil {
			return fmt.Errorf("append work narrative %s: %w", id, err)
		}
		for index, anchor := range record.Anchors {
			_, err = tx.ExecContext(ctx, `INSERT INTO work_narrative_anchors(project_id,narrative_id,ordinal,anchor_kind,target,target_revision,content_sha256,region_locator,authority) VALUES(?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?)`, projectID, id, index, anchor.Kind, anchor.Target, anchor.TargetRevision, anchor.ContentSHA256, anchor.RegionLocator, anchor.Authority)
			if err != nil {
				return fmt.Errorf("append work narrative anchor: %w", err)
			}
		}
	}
	return nil
}

func encodeImportedWorkNarrative(record worknarrative.Record) (string, string, error) {
	type semantic struct {
		Source       *worknarrative.Source  `json:"source,omitempty"`
		Title        string                 `json:"title"`
		Trigger      string                 `json:"trigger"`
		OccurredFrom *time.Time             `json:"occurred_from,omitempty"`
		OccurredTo   *time.Time             `json:"occurred_to,omitempty"`
		Claims       []worknarrative.Claim  `json:"claims"`
		Anchors      []worknarrative.Anchor `json:"anchors"`
	}
	for index := range record.Anchors {
		record.Anchors[index].Drift = ""
	}
	encoded, err := json.Marshal(semantic{Source: record.Source, Title: record.Title, Trigger: record.Trigger, OccurredFrom: record.OccurredFrom, OccurredTo: record.OccurredTo, Claims: record.Claims, Anchors: record.Anchors})
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(encoded)
	semanticHash := hex.EncodeToString(sum[:])
	record.SemanticSHA256 = semanticHash
	record.State = "current"
	record.RecordType = worknarrative.RecordType
	encoded, err = json.Marshal(record)
	if err != nil {
		return "", "", err
	}
	if len(encoded) > worknarrative.MaxRecordBytes {
		return "", "", fmt.Errorf("work narrative exceeds %d bytes", worknarrative.MaxRecordBytes)
	}
	return string(encoded), semanticHash, nil
}

func optionalTimeString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func narrativeReferences(commitID, taskID, invocationID string) []Reference {
	refs := []Reference{{Kind: "commit", ID: commitID}}
	if taskID != "" {
		refs = append(refs, Reference{Kind: "task", ID: taskID})
	}
	if invocationID != "" {
		refs = append(refs, Reference{Kind: "actor_invocation", ID: invocationID})
	}
	return refs
}

func invocationReferences(sessionID, parentID string) []Reference {
	refs := make([]Reference, 0, 2)
	if sessionID != "" {
		refs = append(refs, Reference{Kind: "session", ID: sessionID})
	}
	if parentID != "" {
		refs = append(refs, Reference{Kind: "actor_invocation", ID: parentID})
	}
	return refs
}

func sameReferences(left, right []Reference) bool {
	right = append([]Reference(nil), right...)
	sort.Slice(right, func(i, j int) bool { return referenceLess(right[i], right[j]) })
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validatePortableEffort(value *attribution.Effort) error {
	if value == nil {
		return nil
	}
	if value.Raw == "" || value.Source == "" || value.Authority == "" {
		return fmt.Errorf("raw value, source, and authority are required")
	}
	for _, field := range []string{value.Raw, value.Normalized, value.Source, value.Authority} {
		if !validBoundedText(field, 16<<10) {
			return fmt.Errorf("field exceeds 16 KiB")
		}
	}
	if value.Normalized != "" && !oneOfValue(value.Normalized, "minimal", "low", "medium", "high", "xhigh") {
		return fmt.Errorf("unsupported normalized value %q", value.Normalized)
	}
	return nil
}

func validBoundedText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && strings.IndexByte(value, 0) < 0
}

func parsePortableTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, fmt.Errorf("time must be canonical UTC RFC3339Nano")
	}
	return parsed, nil
}

func parseOptionalPortableTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := parsePortableTime(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func workNarrativeReferences(anchors []worknarrative.Anchor, invocationID string, claims []worknarrative.Claim) []Reference {
	refs := make([]Reference, 0, len(anchors)+1)
	seen := map[Reference]bool{}
	for _, claim := range claims {
		for _, citation := range claim.Citations {
			ref := Reference{Kind: "narrative", ID: strings.TrimPrefix(citation.Source, "narrative:")}
			if !seen[ref] {
				refs = append(refs, ref)
				seen[ref] = true
			}
		}
	}
	for _, anchor := range anchors {
		ref := Reference{Kind: anchor.Kind, ID: anchor.Target}
		if !seen[ref] {
			refs = append(refs, ref)
			seen[ref] = true
		}
	}
	if invocationID != "" {
		ref := Reference{Kind: "actor_invocation", ID: invocationID}
		if !seen[ref] {
			refs = append(refs, ref)
		}
	}
	return refs
}

func oneOfValue(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func effortColumns(value *attribution.Effort) [4]string {
	if value == nil {
		return [4]string{}
	}
	return [4]string{value.Raw, value.Normalized, value.Source, value.Authority}
}

func rowExists(ctx context.Context, tx *sql.Tx, query string, args ...any) (bool, error) {
	var value int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func identityKey(recordType, id string, revision int) string {
	return fmt.Sprintf("%s\x00%s\x00%d", recordType, id, revision)
}

func planItemKey(item ImportPlanItem) string {
	return identityKey(item.RecordType, item.RecordID, item.RecordRevision)
}

type stringHeap []string

func (values stringHeap) Len() int           { return len(values) }
func (values stringHeap) Less(i, j int) bool { return values[i] < values[j] }
func (values stringHeap) Swap(i, j int)      { values[i], values[j] = values[j], values[i] }
func (values *stringHeap) Push(value any)    { *values = append(*values, value.(string)) }
func (values *stringHeap) Pop() any {
	old := *values
	last := old[len(old)-1]
	*values = old[:len(old)-1]
	return last
}

func dependencyOrder[T any](values map[string]T, parent func(T) string) ([]string, error) {
	indegree := make(map[string]int, len(values))
	children := make(map[string][]string)
	ready := &stringHeap{}
	for id, value := range values {
		indegree[id] = 0
		if dependency := parent(value); dependency != "" {
			if _, internal := values[dependency]; internal {
				indegree[id]++
				children[dependency] = append(children[dependency], id)
			}
		}
	}
	for id, degree := range indegree {
		if degree == 0 {
			heap.Push(ready, id)
		}
	}
	ordered := make([]string, 0, len(values))
	for ready.Len() > 0 {
		id := heap.Pop(ready).(string)
		ordered = append(ordered, id)
		for _, child := range children[id] {
			indegree[child]--
			if indegree[child] == 0 {
				heap.Push(ready, child)
			}
		}
	}
	if len(ordered) != len(values) {
		return nil, fmt.Errorf("dependency cycle")
	}
	return ordered, nil
}
