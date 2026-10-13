package app

import (
	"context"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/authorship"
)

// ObserveHeads lets each hook notice commits: when HEAD moved during a tool
// call that ran Git, the new commits are attributed to the session and its
// task. It is best-effort and never fails the hook.
func (a *App) ObserveHeads(ctx context.Context, client, sessionID, eventType, toolName string, toolInput any) {
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
