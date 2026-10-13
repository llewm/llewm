package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/fork"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// ForkResolution is the result of resolving a checkpoint fork.
type ForkResolution struct {
	Resolution   fork.Resolution       `json:"resolution"`
	Continuation checkpoint.Checkpoint `json:"continuation"`
}

// ResolveCheckpointFork settles a task with several checkpoint heads. It writes a
// continuation checkpoint from the accepted head (the proposed, newest head when
// accept is empty) and a resolution naming the rejected heads, atomically. Every
// head stays in history; the continuation becomes the task's tip everywhere it
// syncs.
func (a *App) ResolveCheckpointFork(ctx context.Context, taskID, accept, resolver, authority string) (ForkResolution, error) {
	forks, err := fork.CheckpointForks(ctx, a.DB, a.ProjectID, taskID)
	if err != nil {
		return ForkResolution{}, err
	}
	if len(forks) == 0 {
		return ForkResolution{}, fmt.Errorf("task %q has no unresolved fork", taskID)
	}
	value := forks[0]
	if accept == "" {
		accept = value.Proposed().CheckpointID
	}
	var rejected []string
	found := false
	for _, head := range value.Heads {
		if head.CheckpointID == accept {
			found = true
		} else {
			rejected = append(rejected, head.CheckpointID)
		}
	}
	if !found {
		heads := make([]string, len(value.Heads))
		for index, head := range value.Heads {
			heads[index] = head.CheckpointID
		}
		return ForkResolution{}, fmt.Errorf("%s is not a head of task %q; heads: %s", accept, taskID, strings.Join(heads, ", "))
	}
	accepted, err := checkpoint.Load(ctx, a.DB, a.ProjectID, accept)
	if err != nil || accepted == nil {
		return ForkResolution{}, fmt.Errorf("load accepted head %s: %v", accept, err)
	}
	// The continuation appends after the task's newest checkpoint, as every
	// save must; BasedOn records that it continues the accepted head.
	tip, err := a.Checkpoints.LatestSelected(ctx, a.ProjectID, checkpoint.Selection{TaskID: accepted.TaskID})
	if err != nil || tip == nil {
		return ForkResolution{}, fmt.Errorf("read the tip of task %q: %v", accepted.TaskID, err)
	}
	resolutionID := activity.NewID()
	continuation := checkpoint.Checkpoint{
		ProjectID: a.ProjectID, TaskID: accepted.TaskID, PredecessorCheckpointID: tip.ID, Task: accepted.Task, Status: accepted.Status,
		Summary:    accepted.Summary,
		NextAction: accepted.NextAction, Completed: accepted.Completed, Attempts: accepted.Attempts,
		Changes: accepted.Changes, Verified: accepted.Verified,
		EvidenceRefs: append(append([]string{}, accepted.EvidenceRefs...), "fork_resolution:"+resolutionID),
		Metadata:     accepted.Metadata, BasedOn: accept, CodeHeads: a.codeHeads(ctx),
	}
	if len(continuation.EvidenceRefs) > 64 {
		continuation.EvidenceRefs = continuation.EvidenceRefs[len(continuation.EvidenceRefs)-64:]
	}
	readiness, err := a.Bootstrap.Preflight(ctx, a.ProjectID, a.Root, a.Config.Project.Name, continuation)
	if err != nil {
		return ForkResolution{}, err
	}
	if !readiness.Ready {
		return ForkResolution{}, fmt.Errorf("the accepted head no longer fits the %d-byte bootstrap budget; record a shorter checkpoint for task %q instead", readiness.MaxBytes, taskID)
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return ForkResolution{}, err
	}
	defer tx.Rollback()
	created, err := checkpoint.CreateWithReconciliationTx(ctx, tx, continuation, checkpoint.ActionReconciliation{})
	if err != nil {
		return ForkResolution{}, err
	}
	resolution := fork.Resolution{
		ProjectID: a.ProjectID, ID: resolutionID, Family: fork.FamilyCheckpoint, Subject: taskID,
		AcceptedHead: accept, RejectedHeads: rejected, Continuation: created.Checkpoint.ID,
		Resolver: resolver, Authority: authority, RecordedAt: time.Now().UTC(), SchemaVersion: fork.SchemaVersion,
	}
	if err := fork.Insert(ctx, tx, resolution); err != nil {
		return ForkResolution{}, err
	}
	if err := tx.Commit(); err != nil {
		return ForkResolution{}, err
	}
	if a.AfterCheckpoint != nil {
		a.AfterCheckpoint()
	}
	resolution, _ = fork.Validate(resolution)
	return ForkResolution{Resolution: resolution, Continuation: created.Checkpoint}, nil
}

// maxForkNoticeHeads bounds how many other heads the bootstrap describes.
const maxForkNoticeHeads = 3

// ForkNotice renders unresolved checkpoint forks for the bootstrap: the
// current task's heads in detail, and a count of other forked tasks.
func ForkNotice(forks []fork.Fork, taskID string) string {
	var parts []string
	others := 0
	for _, value := range forks {
		if value.Subject != taskID || taskID == "" {
			others++
			continue
		}
		proposed := value.Proposed()
		part := fmt.Sprintf("task %s has %d checkpoint heads, usually from two machines continuing it before syncing. The newest, %s, is proposed.", taskID, len(value.Heads), proposed.CheckpointID)
		for index, head := range value.Heads[1:] {
			if index == maxForkNoticeHeads {
				part += fmt.Sprintf(" %d more heads (llewm fork show %s).", len(value.Heads)-1-maxForkNoticeHeads, value.Ref())
				break
			}
			part += fmt.Sprintf(" Other head %s (%s): next %q.", head.CheckpointID, head.CreatedAt.UTC().Format("2006-01-02 15:04Z"), bounded(head.NextAction, 200))
		}
		part += fmt.Sprintf(" Both are kept. `llewm fork resolve %s` continues from the proposed head (`--accept ID` picks another).", value.Ref())
		parts = append(parts, part)
	}
	if others > 0 {
		parts = append(parts, fmt.Sprintf("%d other tasks have unresolved checkpoint forks (llewm fork list).", others))
	}
	return strings.Join(parts, " ")
}

// RevisionForkNotice counts unresolved revision forks for the bootstrap and
// names the first few.
func RevisionForkNotice(forks []fork.RevisionFork) string {
	if len(forks) == 0 {
		return ""
	}
	var refs []string
	for index, value := range forks {
		if index == maxForkNoticeHeads {
			refs = append(refs, fmt.Sprintf("%d more", len(forks)-maxForkNoticeHeads))
			break
		}
		refs = append(refs, value.Ref())
	}
	return fmt.Sprintf("%d record revisions have several versions, usually from two machines writing before syncing: %s. Each machine keeps its own until `llewm fork resolve TYPE:ID@REVISION` appends the accepted version (llewm fork show).", len(forks), strings.Join(refs, ", "))
}

func bounded(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// RevisionForkResolution is the result of resolving a revision fork.
type RevisionForkResolution struct {
	Resolution fork.Resolution `json:"resolution"`
	// Revision is the revision written with the accepted content, or 0 when the
	// project's own later revisions already continue the accepted head.
	Revision int `json:"revision,omitempty"`
}

// ResolveRevisionFork settles a record revision that machines wrote with
// different content. accept names the accepted head by content hash (a unique
// prefix of at least 7 characters), default the proposed, newest head. The
// accepted content is appended as the record's next revision, so every machine
// converges on it whichever version it held, together with a resolution naming
// the rejected heads, atomically. When the accepted head is the project's own
// version and later revisions already continue it, only the resolution is
// written. Every head stays in history.
func (a *App) ResolveRevisionFork(ctx context.Context, subject, accept, resolver, authority string) (RevisionForkResolution, error) {
	recordType, recordID, _, err := fork.ParseRevisionSubject(subject)
	if err != nil {
		return RevisionForkResolution{}, err
	}
	forks, err := fork.RevisionForks(ctx, a.DB, a.ProjectID, subject)
	if err != nil {
		return RevisionForkResolution{}, err
	}
	if len(forks) == 0 {
		return RevisionForkResolution{}, fmt.Errorf("%s has no unresolved fork", subject)
	}
	value := forks[0]
	var accepted *fork.RevisionHead
	var rejected, hashes []string
	for index, head := range value.Heads {
		hashes = append(hashes, head.ContentSHA256)
		if (accept == "" && index == 0) || (accept != "" && len(accept) >= 7 && strings.HasPrefix(head.ContentSHA256, accept)) {
			if accepted != nil {
				return RevisionForkResolution{}, fmt.Errorf("%s matches several heads of %s", accept, subject)
			}
			accepted = &value.Heads[index]
		} else {
			rejected = append(rejected, head.ContentSHA256)
		}
	}
	if accepted == nil {
		return RevisionForkResolution{}, fmt.Errorf("%q is not a head of %s (name one by at least 7 characters of its hash); heads: %s", accept, subject, strings.Join(hashes, ", "))
	}
	versions, err := portable.RecordVersions(ctx, a.DB, a.ProjectID, recordType, recordID)
	if err != nil {
		return RevisionForkResolution{}, err
	}
	if len(versions) == 0 {
		return RevisionForkResolution{}, fmt.Errorf("the project has no revision of %s:%s", recordType, recordID)
	}
	latest := versions[len(versions)-1].Envelope.RecordRevision
	resolution := fork.Resolution{
		ProjectID: a.ProjectID, ID: activity.NewID(), Family: fork.FamilyRevision, Subject: value.Ref(),
		AcceptedHead: accepted.ContentSHA256, RejectedHeads: rejected,
		Resolver: resolver, Authority: authority, RecordedAt: time.Now().UTC(), SchemaVersion: fork.RevisionSchemaVersion,
	}
	if accepted.Local && latest > value.Revision {
		if err := fork.Insert(ctx, a.DB, resolution); err != nil {
			return RevisionForkResolution{}, err
		}
		if a.AfterCheckpoint != nil {
			a.AfterCheckpoint()
		}
		resolution, _ = fork.Validate(resolution)
		return RevisionForkResolution{Resolution: resolution}, nil
	}
	body, err := fork.HeadEnvelope(ctx, a.DB, a.ProjectID, recordType, recordID, value.Revision, accepted.ContentSHA256)
	if err != nil {
		return RevisionForkResolution{}, fmt.Errorf("load accepted head: %w", err)
	}
	spec, err := portable.DecodeStoredMemoryEnvelope(body)
	if err != nil {
		return RevisionForkResolution{}, fmt.Errorf("decode accepted head: %w", err)
	}
	spec.RecordRevision, spec.RecordedAt = latest+1, resolution.RecordedAt
	resolution.Continuation = fork.RevisionSubject(recordType, recordID, spec.RecordRevision)
	plan, _, err := portable.ImportMemoryRecordsWithin(ctx, a.DB, a.ProjectID, []portable.EnvelopeSpec{spec}, resolution.RecordedAt, func(ctx context.Context, tx *sql.Tx) error {
		return fork.Insert(ctx, tx, resolution)
	})
	if err != nil {
		return RevisionForkResolution{}, err
	}
	if !plan.ApplyAllowed || plan.Counts[portable.PlanAppend] != 1 {
		reason := "nothing to append"
		if len(plan.Items) > 0 && plan.Items[0].Reason != "" {
			reason = plan.Items[0].Reason
		}
		return RevisionForkResolution{}, fmt.Errorf("cannot append %s: %s", resolution.Continuation, reason)
	}
	if a.AfterCheckpoint != nil {
		a.AfterCheckpoint()
	}
	resolution, _ = fork.Validate(resolution)
	return RevisionForkResolution{Resolution: resolution, Revision: spec.RecordRevision}, nil
}

// ResolveSupersessionFork settles a record (FAMILY:ID) that several records
// superseded. accept names the accepted head by record ID, default the
// proposed, newest head. Every successor already exists on every machine, so
// only a resolution naming the rejected heads is written; it syncs, and each
// machine then reads the rejected heads as superseded by the accepted one.
func (a *App) ResolveSupersessionFork(ctx context.Context, subject, accept, resolver, authority string) (fork.Resolution, error) {
	forks, err := fork.SupersessionForks(ctx, a.DB, a.ProjectID, subject)
	if err != nil {
		return fork.Resolution{}, err
	}
	if len(forks) == 0 {
		return fork.Resolution{}, fmt.Errorf("%s has no unresolved fork", strings.TrimSpace(subject))
	}
	value := forks[0]
	if accept == "" {
		accept = value.Proposed().ID
	}
	var rejected, heads []string
	found := false
	for _, head := range value.Heads {
		heads = append(heads, head.ID)
		if head.ID == accept {
			found = true
		} else {
			rejected = append(rejected, head.ID)
		}
	}
	if !found {
		return fork.Resolution{}, fmt.Errorf("%s is not a head of %s; heads: %s", accept, value.Ref(), strings.Join(heads, ", "))
	}
	resolution := fork.Resolution{
		ProjectID: a.ProjectID, ID: activity.NewID(), Family: fork.FamilySupersession, Subject: value.Ref(),
		AcceptedHead: accept, RejectedHeads: rejected,
		Resolver: resolver, Authority: authority, RecordedAt: time.Now().UTC(), SchemaVersion: fork.SchemaVersion,
	}
	if err := fork.Insert(ctx, a.DB, resolution); err != nil {
		return fork.Resolution{}, err
	}
	if a.AfterCheckpoint != nil {
		a.AfterCheckpoint()
	}
	resolution, _ = fork.Validate(resolution)
	return resolution, nil
}

// SupersessionForkNotice counts unresolved supersession forks for the
// bootstrap and names the first few.
func SupersessionForkNotice(forks []fork.SupersessionFork) string {
	if len(forks) == 0 {
		return ""
	}
	var refs []string
	for index, value := range forks {
		if index == maxForkNoticeHeads {
			refs = append(refs, fmt.Sprintf("%d more", len(forks)-maxForkNoticeHeads))
			break
		}
		refs = append(refs, value.Ref())
	}
	return fmt.Sprintf("%d records were superseded more than once, usually from two machines writing before syncing: %s. Every successor is kept and reads as current until `llewm fork resolve FAMILY:ID` accepts one (llewm fork show).", len(forks), strings.Join(refs, ", "))
}

// ResolveStaleWrite settles a write made under a superseded lease token
// (stale:RECORD_KIND:RECORD_ID). accept is lease.AcceptLease (default: the
// write yields to the newer lease) or lease.AcceptWrite (it stands). Only the
// resolution is written; the record stays in history either way, and the
// resolution syncs so every machine stops listing the write.
func (a *App) ResolveStaleWrite(ctx context.Context, subject, accept, resolver, authority string) (lease.StaleResolution, error) {
	kind, id, err := lease.ParseStaleSubject(subject)
	if err != nil {
		return lease.StaleResolution{}, err
	}
	writes, err := lease.StaleWrites(ctx, a.DB, a.ProjectID, lease.StaleSubject(kind, id))
	if err != nil {
		return lease.StaleResolution{}, err
	}
	if len(writes) == 0 {
		return lease.StaleResolution{}, fmt.Errorf("%s has no unresolved stale write", lease.StaleSubject(kind, id))
	}
	if accept == "" {
		accept = lease.AcceptLease
	}
	value := lease.StaleResolution{ProjectID: a.ProjectID, ID: activity.NewID(), RecordKind: kind, RecordID: id, Accepted: accept, Resolver: resolver, Authority: authority, RecordedAt: time.Now().UTC(), SchemaVersion: lease.StampSchemaVersion}
	if err := lease.InsertStaleResolution(ctx, a.DB, value); err != nil {
		return lease.StaleResolution{}, err
	}
	if a.AfterCheckpoint != nil {
		a.AfterCheckpoint()
	}
	return lease.ValidateStaleResolution(value)
}
