package app

import (
	"context"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/authorship"
)

// ObserveHeads lets each hook notice commits: when HEAD moved during a tool
// call that ran Git, newly reachable commits with timely commit-creating reflog
// evidence are attributed to the session and its task. It is best-effort and
// never fails the hook.
func (a *App) ObserveHeads(ctx context.Context, client, sessionID, eventType, toolName string, toolInput any) {
	a.revalidateAuthorship(ctx)
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

// revalidateAuthorship runs the one-time repair of pre-fix attributions that
// migration 097 requests, before anything reads or adds attributions. It is
// best-effort: a failure leaves the request for the next call.
func (a *App) revalidateAuthorship(ctx context.Context) {
	var checkouts []authorship.Checkout
	for _, repository := range a.Repositories.All() {
		checkouts = append(checkouts, authorship.Checkout{Member: repository.Member, Root: repository.Root})
	}
	_, _ = authorship.RevalidatePending(ctx, a.DB, a.ProjectID, checkouts, time.Now())
}
