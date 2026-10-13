package app

import (
	"context"
	"database/sql"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/presence"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

// SessionTask is the task this session works on: its newest live presence task,
// else the task it last checkpointed, else "". Four-hour coordination leases
// deliberately do not claim that a session is alive.
func (a *App) SessionTask(ctx context.Context, sessionID string) (string, error) {
	report, err := a.Presence.List(ctx, a.ProjectID, presence.Query{SessionID: sessionID, LiveOnly: true, Limit: presence.MaxLimit, Now: time.Now().UTC()})
	if err != nil {
		return "", err
	}
	for _, value := range report.Presence {
		if value.TaskID == "" {
			continue
		}
		if latest, err := a.Checkpoints.LatestSelected(ctx, a.ProjectID, checkpoint.Selection{TaskID: value.TaskID}); err != nil {
			return "", err
		} else if latest != nil {
			return value.TaskID, nil
		}
	}
	return checkpoint.SessionTask(ctx, a.DB, a.ProjectID, sessionID)
}

// chooseTask picks the bootstrap's task: an explicit selection as given; else
// the task this session has live presence on, then the task it last checkpointed,
// then the newest task last checkpointed on a current branch, then the
// project-wide newest checkpoint, unless several in-progress tasks contend
// (see bootstrap.Choose). It also lists the other active tasks. It returns nil
// on failure, which keeps the selection.
func chooseTask(ctx context.Context, db *sql.DB, repositories project.Repositories, projectID, sessionID string, selection checkpoint.Selection, now time.Time) *bootstrap.Choice {
	tips, err := checkpoint.TaskTips(ctx, db, projectID)
	if err != nil {
		return nil
	}
	closed := func(taskID string) bool {
		closed, err := taskUnavailableForImplicitSelection(ctx, db, projectID, taskID)
		return err == nil && closed
	}
	var choice bootstrap.Choice
	if selection != (checkpoint.Selection{}) {
		choice = bootstrap.Choice{Selection: selection, Rule: bootstrap.RuleExplicit}
	} else {
		presenceReport, err := (presence.Service{DB: db}).List(ctx, projectID, presence.Query{LiveOnly: true, Limit: presence.MaxLimit, Now: now})
		if err != nil {
			return nil
		}
		var present []string
		for _, value := range presenceReport.Presence {
			if value.SessionID == sessionID && value.TaskID != "" {
				present = append(present, value.TaskID)
			}
		}
		var current []checkpoint.CodeBranch
		for _, head := range observeCodeHeads(ctx, repositories) {
			current = append(current, checkpoint.CodeBranch{Member: head.Member, Branch: head.Branch})
		}
		sessionTask, err := checkpoint.SessionTask(ctx, db, projectID, sessionID)
		if err != nil {
			return nil
		}
		presentByOthers := map[string]string{}
		for _, value := range presenceReport.Presence {
			if value.TaskID != "" && (sessionID == "" || value.SessionID != sessionID) {
				presentByOthers[value.TaskID] = value.Client + " (session " + value.SessionID + ")"
			}
		}
		choice = bootstrap.Choose(tips, present, current, bootstrap.ChooseContext{SessionTask: sessionTask, Now: now, Closed: closed, PresentByOthers: func(taskID string) string { return presentByOthers[taskID] }})
		if choice.Rule == bootstrap.RuleAmbiguous {
			return &choice
		}
	}
	chosen := choice.Selection.TaskID
	if chosen == "" {
		// The chosen checkpoint decides the task; an explicit checkpoint or
		// session selection, or the newest rule, names none up front.
		latest, err := (checkpoint.Service{DB: db}).LatestSelected(ctx, projectID, choice.Selection)
		if err != nil {
			return nil
		}
		if latest != nil {
			chosen = latest.TaskID
		}
	}
	choice.Others, choice.OthersOmitted = bootstrap.OtherActive(tips, chosen, closed)
	return &choice
}

// taskUnavailableForImplicitSelection keeps explicit historical reads possible
// while preventing presence, session, branch, and newest routing from reviving a
// task whose lifecycle ended or whose current roadmap node is not open work.
func taskUnavailableForImplicitSelection(ctx context.Context, db *sql.DB, projectID, taskID string) (bool, error) {
	closed, err := checkpoint.TaskClosed(ctx, db, projectID, taskID)
	if err != nil || closed {
		return closed, err
	}
	var mapped, open int
	err = db.QueryRowContext(ctx, `WITH mapped(node_id) AS (
		SELECT b.roadmap_node_id FROM roadmap_task_bindings b
		WHERE b.project_id=? AND b.task_id=? AND b.status='active'
		AND b.revision=(SELECT MAX(x.revision) FROM roadmap_task_bindings x WHERE x.project_id=b.project_id AND x.roadmap_node_id=b.roadmap_node_id AND x.task_id=b.task_id)
		UNION SELECT ? WHERE EXISTS(SELECT 1 FROM roadmap_nodes n WHERE n.project_id=? AND n.node_id=?)
	), current_nodes AS (
		SELECT n.node_id,n.status FROM roadmap_nodes n WHERE n.project_id=?
		AND n.revision=(SELECT MAX(x.revision) FROM roadmap_nodes x WHERE x.project_id=n.project_id AND x.node_id=n.node_id)
	) SELECT COUNT(*),COALESCE(SUM(CASE WHEN n.status IN ('accepted','active','blocked') THEN 1 ELSE 0 END),0)
	FROM mapped m JOIN current_nodes n ON n.node_id=m.node_id`, projectID, taskID, taskID, projectID, taskID, projectID).Scan(&mapped, &open)
	if err != nil {
		return false, err
	}
	return mapped > 0 && open == 0, nil
}
