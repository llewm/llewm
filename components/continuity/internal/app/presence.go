package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/pin"
	"github.com/llewm/llewm/components/continuity/internal/presence"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

// ObservePresence translates a supported client or MCP lifecycle boundary into
// one short-lived device-local presence transition. Callers keep it best-effort:
// activity ingestion and tool handling must not depend on advisory presence.
func (a *App) ObservePresence(ctx context.Context, client, sessionID, invocationID, eventType, cwd string, at time.Time) error {
	if sessionID == "" {
		return nil
	}
	_, _ = a.Presence.Expire(ctx, a.ProjectID, at)
	state := presenceState(eventType)
	taskID, _ := a.SessionTask(ctx, sessionID)
	roadmapNodeID := ""
	if taskID != "" {
		if nodes, err := pin.TaskRoadmapNodes(ctx, a.DB, a.ProjectID, taskID); err == nil && len(nodes) > 0 {
			roadmapNodeID = nodes[0]
		}
	}
	checkoutPath, worktreePath, branch := a.presenceCheckout(ctx, cwd)
	claimID := a.presenceClaim(ctx, client, sessionID, at)
	_, _, err := a.Presence.Observe(ctx, presence.Observation{
		ProjectID: a.ProjectID, ClaimID: claimID, Actor: client, Client: client, SessionID: sessionID, InvocationID: invocationID,
		TaskID: taskID, RoadmapNodeID: roadmapNodeID, State: state, Authority: "machine_observed",
		CheckoutPath: checkoutPath, WorktreePath: worktreePath, Branch: branch, At: at,
	})
	if strings.EqualFold(eventType, "sessionend") {
		err = errors.Join(err, a.releaseSessionLeases(ctx, sessionID))
	}
	return err
}

// releaseSessionLeases closes only this session's grants on this instance.
// Lifecycle hooks must finish without a network round trip; ordinary lease
// synchronization later publishes the append-only releases to the remote.
// Stop/yield is a turn boundary, so it deliberately keeps ownership.
func (a *App) releaseSessionLeases(ctx context.Context, sessionID string) error {
	local := a.Leases
	local.Remote = nil
	values, err := local.List(ctx, a.ProjectID, true)
	if err != nil {
		return err
	}
	var result error
	for _, value := range values {
		if value.SessionID != sessionID || value.State == lease.Released {
			continue
		}
		_, err := local.Release(ctx, lease.Request{ProjectID: a.ProjectID, ScopeKind: value.ScopeKind, ScopeID: value.ScopeID, Holder: value.Holder, SessionID: sessionID, Token: value.Token})
		// A remote instance or newer grant can share the session ID. Release
		// validates instance, holder and fencing token in its transaction.
		if err != nil && !errors.Is(err, lease.ErrNotHeld) {
			result = errors.Join(result, err)
		}
	}
	return result
}

// ObserveTaskPresence updates the session's exact task after a checkpoint,
// including terminal/yielded task state that ordinary client hooks cannot know.
func (a *App) ObserveTaskPresence(ctx context.Context, client, sessionID, taskID, checkpointStatus string, at time.Time) error {
	if sessionID == "" || taskID == "" {
		return nil
	}
	state := "active"
	switch checkpointStatus {
	case "completed":
		state = "completed"
	case "paused", "blocked":
		state = "yielded"
	}
	nodes, _ := pin.TaskRoadmapNodes(ctx, a.DB, a.ProjectID, taskID)
	roadmapNodeID := ""
	if len(nodes) > 0 {
		roadmapNodeID = nodes[0]
	}
	checkoutPath, worktreePath, branch := a.presenceCheckout(ctx, a.Root)
	claimID := a.presenceClaim(ctx, client, sessionID, at)
	_, _, err := a.Presence.Observe(ctx, presence.Observation{
		ProjectID: a.ProjectID, ClaimID: claimID, Actor: client, Client: client, SessionID: sessionID, TaskID: taskID,
		RoadmapNodeID: roadmapNodeID, State: state, Authority: "machine_observed",
		CheckoutPath: checkoutPath, WorktreePath: worktreePath, Branch: branch, At: at,
	})
	return err
}

// presenceClaim lets a checkpoint made before the first client hook establish
// a placeholder claim, then adopts that same append-only claim once the hook
// supplies the exact client identity. Exact matching client claims always win.
func (a *App) presenceClaim(ctx context.Context, client, sessionID string, now time.Time) string {
	report, err := a.Presence.List(ctx, a.ProjectID, presence.Query{SessionID: sessionID, Limit: presence.MaxLimit, Now: now})
	if err != nil {
		return ""
	}
	for _, value := range report.Presence {
		if value.Client == client {
			return value.ClaimID
		}
	}
	if len(report.Presence) == 1 && report.Presence[0].Client == "agent" {
		return report.Presence[0].ClaimID
	}
	return ""
}

func presenceState(eventType string) string {
	switch strings.ToLower(eventType) {
	case "stop", "sessionyield", "sessionpause":
		return "yielded"
	case "sessionend":
		return "completed"
	case "sessioncancel", "cancelled":
		return "cancelled"
	default:
		return "active"
	}
}

func (a *App) presenceCheckout(ctx context.Context, cwd string) (string, string, string) {
	if cwd == "" {
		cwd = a.Root
	}
	checkout, err := project.Resolve(cwd)
	if err != nil || checkout.Root != a.Root {
		return "", "", ""
	}
	return checkout.Root, checkout.Path, currentBranch(ctx, checkout.Path)
}
