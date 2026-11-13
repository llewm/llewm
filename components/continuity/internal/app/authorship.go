package app

import (
	"context"
	"database/sql"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/authorship"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

// ObserveHeads lets each hook notice commits: when HEAD moved during a tool
// call that ran Git, newly reachable commits with timely commit-creating reflog
// evidence are attributed to the session and its task. It is best-effort and
// never fails the hook.
func (a *App) ObserveHeads(ctx context.Context, client, sessionID, eventType, toolName string, toolInput any) {
	a.repairAuthorship(ctx)
	attribute := eventType == "posttooluse" && authorship.RanGitWrite(toolInput)
	taskID := ""
	if attribute {
		taskID, _ = a.SessionTask(ctx, sessionID)
	}
	for _, repository := range a.Repositories.All() {
		_, _ = authorship.Observe(ctx, a.DB, authorship.Observation{
			ProjectID: a.ProjectID, Member: repository.Member, Root: repository.Root,
			SessionID: sessionID, Client: client, TaskID: taskID, ToolName: toolName,
			Attribute: attribute, At: time.Now(),
		})
	}
}

// repairAuthorship runs the one-time repairs of pre-fix attributions that
// migrations 097 and 098 request, before anything reads or adds attributions.
// It is best-effort: a failure leaves the request for the next call.
func (a *App) repairAuthorship(ctx context.Context) {
	var checkouts []authorship.Checkout
	for _, repository := range a.Repositories.All() {
		checkouts = append(checkouts, authorship.Checkout{Member: repository.Member, Root: repository.Root})
	}
	_, _ = authorship.RepairPending(ctx, a.DB, a.ProjectID, checkouts, time.Now())
}

// retaskSessionCommitsTx gives the saving session's task-less commits, observed
// since its previous checkpoint, the task this checkpoint names. It runs in
// the checkpoint's transaction and is best-effort: attribution never fails a
// save.
func retaskSessionCommitsTx(ctx context.Context, tx *sql.Tx, saved checkpoint.Checkpoint) {
	if saved.SessionID == "" || saved.TaskID == "" {
		return
	}
	checkpoints, err := checkpoint.SessionCheckpoints(ctx, tx, saved.ProjectID, saved.SessionID)
	if err != nil {
		return
	}
	var since time.Time
	for _, value := range checkpoints {
		if value.ID != saved.ID && value.CreatedAt.Before(saved.CreatedAt) {
			since = value.CreatedAt
		}
	}
	_, _ = authorship.RetaskSession(ctx, tx, saved.ProjectID, saved.SessionID, saved.TaskID, since, saved.CreatedAt, time.Now())
}
