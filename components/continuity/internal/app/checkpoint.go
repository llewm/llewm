package app

import (
	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/config"

	"context"
	"errors"
	"fmt"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/authorship"
	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/researchcapture"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

// Fence pairs a writer's lease claim with this project's policy for the scope.
func (a *App) Fence(scopeKind, scopeID string, claim lease.Claim) lease.Fence {
	return lease.Fence{Claim: claim, Required: a.LeasePolicy.Required(scopeKind, scopeID)}
}

// AppendPlan records a plan revision under the writer's lease claim in
// value.Fence.Claim, refusing it when the plan's scope requires a lease the
// claim does not hold.
func (a *App) AppendPlan(ctx context.Context, value trackplan.Plan) (trackplan.Plan, error) {
	value.Fence = a.Fence("track_plan", value.ID, value.Fence.Claim)
	created, err := a.TrackPlans.AppendPlan(ctx, value)
	if err != nil || created.TaskID == "" || created.RoadmapNodeID == "" {
		return created, err
	}
	if created.Status == "proposed" {
		operative, err := a.TrackPlans.EffectivePlan(ctx, a.ProjectID, created.ID)
		if err != nil {
			return created, err
		}
		if operative != nil && operative.Status != "proposed" {
			return created, nil
		}
	}
	// A plan naming its task and node binds them, so the track shows the
	// task without a separate step. Best-effort: the plan stands either way,
	// and work check reports a task left unbound.
	_, _ = a.Roadmap.EnsureTaskBinding(ctx, a.ProjectID, created.RoadmapNodeID, created.TaskID,
		"bound by plan:"+created.ID, fmt.Sprintf("plan-%s-%d-binds-%s", created.ID, created.Revision, created.TaskID))
	return created, nil
}

// AcceptPlan records a human review of a plan's latest revision under the
// writer's lease claim.
func (a *App) AcceptPlan(ctx context.Context, id, status, reviewer string, claim lease.Claim) (trackplan.Plan, error) {
	created, err := a.TrackPlans.Accept(ctx, a.ProjectID, id, status, reviewer, a.Fence("track_plan", id, claim))
	if err == nil && created.TaskID != "" && created.RoadmapNodeID != "" {
		_, _ = a.Roadmap.EnsureTaskBinding(ctx, a.ProjectID, created.RoadmapNodeID, created.TaskID, "bound by plan:"+created.ID, fmt.Sprintf("plan-%s-%d-binds-%s", created.ID, created.Revision, created.TaskID))
	}
	return created, err
}

// UnnarratedCommits is the best-effort narrative coverage check reported after
// checkpoints and at session start; a failure reports nothing rather than
// displacing the caller's result.
func (a *App) UnnarratedCommits(ctx context.Context) []worknarrative.UnnarratedCommit {
	commits, err := a.Narratives.UnnarratedCommits(ctx, a.ProjectID)
	if err != nil {
		return nil
	}
	for i := range commits {
		authors, err := authorship.Authors(ctx, a.DB, a.ProjectID, commits[i].Member, []string{commits[i].Commit})
		if err != nil {
			continue
		}
		if author, ok := authors[commits[i].Commit]; ok {
			commits[i].AuthorTask, commits[i].AuthorSession = author.TaskID, author.SessionID
		}
	}
	return commits
}

// ResearchCapture is the best-effort task-boundary advisory for a saved
// checkpoint. A read failure reports nothing rather than displacing the
// checkpoint receipt.
func (a *App) ResearchCapture(ctx context.Context, candidate checkpoint.Checkpoint) *researchcapture.Report {
	report, err := (researchcapture.Service{DB: a.DB}).ReportForCheckpoint(ctx, a.ProjectID, researchcapture.Candidate{
		SessionID: candidate.SessionID, TaskID: candidate.TaskID, PredecessorCheckpointID: candidate.PredecessorCheckpointID,
		EvidenceRefs: len(candidate.EvidenceRefs), Completed: len(candidate.Completed), Verified: len(candidate.Verified), Attempts: len(candidate.Attempts),
	})
	if err != nil {
		return nil
	}
	return report
}

// SaveCheckpoint validates the exact prospective bootstrap before persisting
// the checkpoint and its action lifecycle records. Preflight remains available
// for byte accounting, but callers cannot bypass the startup budget by calling
// the write surface directly.
func (a *App) SaveCheckpoint(ctx context.Context, candidate checkpoint.Checkpoint) (checkpoint.CreateResult, error) {
	return a.SaveCheckpointWithReconciliation(ctx, candidate, checkpoint.ActionReconciliation{})
}

// StageCheckpointRecovery persists only the validated structured checkpoint
// fields needed to retry a writer-incompatible save after restart.
func (a *App) StageCheckpointRecovery(candidate checkpoint.Checkpoint, reconciliation checkpoint.ActionReconciliation) (checkpoint.RecoveryEnvelope, error) {
	return a.CheckpointRecovery.Stage(candidate, reconciliation, buildinfo.WriterAPIVersion, buildinfo.MCPAPIVersion)
}

// ApplyCheckpointRecovery saves one staged envelope with current semantics and
// removes it only after the save succeeds and readback confirms its ID.
func (a *App) ApplyCheckpointRecovery(ctx context.Context, id string) (checkpoint.CreateResult, error) {
	envelope, err := a.CheckpointRecovery.Get(id)
	if err != nil {
		return checkpoint.CreateResult{}, err
	}
	created, err := a.saveCheckpointWithReconciliation(ctx, envelope.Candidate, envelope.Reconciliation, false)
	if err != nil {
		return checkpoint.CreateResult{}, err
	}
	readback, err := a.Checkpoints.Get(ctx, a.ProjectID, created.Checkpoint.ID, checkpoint.DefaultExportBytes)
	if err != nil {
		return checkpoint.CreateResult{}, fmt.Errorf("checkpoint %s was saved but readback failed; recovery envelope %s was retained: %w", created.Checkpoint.ID, id, err)
	}
	if readback == nil || readback.ID != created.Checkpoint.ID {
		return checkpoint.CreateResult{}, fmt.Errorf("checkpoint %s was saved but readback did not confirm it; recovery envelope %s was retained", created.Checkpoint.ID, id)
	}
	if err := a.CheckpointRecovery.Remove(id); err != nil {
		return checkpoint.CreateResult{}, fmt.Errorf("checkpoint %s was saved but recovery envelope %s could not be removed: %w", created.Checkpoint.ID, id, err)
	}
	return created, nil
}

// CheckWriter refuses writes from a process older than the newest build that
// has opened the store (a long-running MCP server started before an upgrade).
func (a *App) CheckWriter(ctx context.Context) error {
	if err := adoption.CheckActive(config.StateDir(a.Root)); err != nil {
		return err
	}
	if a.DB == nil {
		return nil
	}
	return database.CheckWriter(ctx, a.DB, buildinfo.WriterAPIVersion)
}

// SaveCheckpointWithReconciliation validates the exact prospective bootstrap and
// action lifecycle result before persisting both atomically.
func (a *App) SaveCheckpointWithReconciliation(ctx context.Context, candidate checkpoint.Checkpoint, reconciliation checkpoint.ActionReconciliation) (checkpoint.CreateResult, error) {
	return a.saveCheckpointWithReconciliation(ctx, candidate, reconciliation, true)
}

// saveCheckpointWithReconciliation optionally refreshes the saving session's
// ephemeral coordination state. Recovery applies a checkpoint attributed to a
// process that was explicitly restarted, so it must not revive that historical
// session's presence or renew its advisory task lease.
func (a *App) saveCheckpointWithReconciliation(ctx context.Context, candidate checkpoint.Checkpoint, reconciliation checkpoint.ActionReconciliation, coordinateSession bool) (checkpoint.CreateResult, error) {
	if err := a.CheckWriter(ctx); err != nil {
		return checkpoint.CreateResult{}, err
	}
	candidate.ProjectID = a.ProjectID
	candidate.CodeHeads = a.codeHeads(ctx)
	candidate.Fence = a.Fence("task", candidate.TaskID, candidate.Fence.Claim)
	replayed, err := a.Checkpoints.ReplayRequest(ctx, candidate, reconciliation)
	if err != nil {
		return checkpoint.CreateResult{}, err
	}
	if replayed != nil {
		return *replayed, nil
	}
	readiness, err := a.Bootstrap.PreflightWithReconciliation(ctx, a.ProjectID, a.Root, a.Config.Project.Name, candidate, reconciliation)
	if err != nil {
		return checkpoint.CreateResult{}, err
	}
	if !readiness.Ready {
		return checkpoint.CreateResult{}, fmt.Errorf(
			"checkpoint bootstrap exceeds %d-byte budget (json=%d, markdown=%d, oversize_by=%d); shorten the checkpoint before saving",
			readiness.MaxBytes, readiness.JSONBytes, readiness.MarkdownBytes, readiness.OversizeByBytes,
		)
	}
	created, err := a.Checkpoints.CreateWithReconciliation(ctx, candidate, reconciliation)
	if err == nil && coordinateSession && candidate.SessionID != "" && candidate.Fence.Claim.Token == 0 {
		created.SessionLease = a.sessionLease(ctx, candidate.TaskID, candidate.SessionID, candidate.Status == "completed")
	}
	if err == nil && a.AfterCheckpoint != nil {
		a.AfterCheckpoint()
	}
	if err == nil && coordinateSession && candidate.SessionID != "" {
		client := a.sessionClient(ctx, candidate.SessionID)
		_ = a.ObserveTaskPresence(ctx, client, candidate.SessionID, candidate.TaskID, candidate.Status, time.Now().UTC())
	}
	return created, err
}

// SessionLeaseTTL is how long an automatic session lease lasts after a
// checkpoint; each later checkpoint of the session renews it.
const SessionLeaseTTL = 4 * time.Hour

// sessionLease keeps an advisory lease on the task for the checkpoint's
// session: taken or renewed on an unfinished checkpoint, released on a
// completed one. It is best-effort and local (a later sync publishes it), so a
// save never waits on the network or fails over a lease. Another session's
// lease is reported, not taken over.
func (a *App) sessionLease(ctx context.Context, taskID, sessionID string, completed bool) string {
	local := a.Leases
	local.Remote = nil
	holder := a.sessionClient(ctx, sessionID)
	scope := "task:" + taskID
	if completed {
		value, err := lease.CurrentLease(ctx, a.DB, a.ProjectID, "task", taskID, time.Now().UTC())
		if err != nil || value == nil || value.State != lease.Held || value.SessionID != sessionID {
			return ""
		}
		if _, err := local.Release(ctx, lease.Request{ProjectID: a.ProjectID, ScopeKind: "task", ScopeID: taskID, Holder: value.Holder, SessionID: sessionID, Token: value.Token}); err != nil {
			return "automatic release of " + scope + " failed: " + err.Error()
		}
		return "released " + scope + ": the task is completed"
	}
	result, err := local.Acquire(ctx, lease.Request{ProjectID: a.ProjectID, ScopeKind: "task", ScopeID: taskID, Holder: holder, SessionID: sessionID, TTL: SessionLeaseTTL})
	var conflict *lease.ConflictError
	switch {
	case errors.As(err, &conflict):
		return scope + " is leased by " + conflict.Current.HolderLabel() + "; this checkpoint was saved without a lease. Coordinate before continuing that agent's task."
	case err != nil:
		return "automatic lease of " + scope + " failed: " + err.Error()
	}
	return fmt.Sprintf("%s leased to this session until %s (token %d)", scope, result.Lease.ValidUntil.UTC().Format(time.RFC3339), result.Lease.Token)
}

// sessionClient names the client a session's hook events came from.
func (a *App) sessionClient(ctx context.Context, sessionID string) string {
	var client string
	if err := a.DB.QueryRowContext(ctx, `SELECT client FROM events WHERE project_id=? AND session_id=? ORDER BY received_at LIMIT 1`, a.ProjectID, sessionID).Scan(&client); err != nil || client == "" {
		return "agent"
	}
	return client
}
