package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/readiness"
	"github.com/llewm/llewm/components/continuity/internal/release"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
)

// consoleData is a versioned presentation-neutral read envelope. The CLI and
// terminal consume the same canonical projections, with no command execution.
type consoleData struct {
	SchemaVersion int    `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	View          string `json:"view"`
	Data          any    `json:"data"`
}

func (s appTerminalSource) Project(ctx context.Context) (projectView, error) {
	checkout := s.checkout
	var err error
	if checkout.Root == "" {
		checkout, err = project.ResolveCheckout(s.app.Root)
	}
	if err != nil {
		return projectView{}, err
	}
	return readProjectView(ctx, s.app, checkout, 10)
}

func readProjectView(ctx context.Context, a *app.App, checkout project.Checkout, limit int) (projectView, error) {
	locations, err := project.Locations(ctx, a.DB, a.ProjectID, limit)
	if err != nil {
		return projectView{}, err
	}
	v := projectView{SchemaVersion: 1, ProjectID: a.ProjectID, Name: a.Config.Project.Name, Root: a.Root, Checkout: checkout, Identity: a.Identity, Database: a.Config.Storage.Database, StoreInside: project.StoreInside(a.Config.Storage.Database, a.Root), Locations: locations, Workspace: a.Workspace}
	if a.Workspace != nil {
		if a.Workspace.Mode == project.ModeFederated {
			globalRoot, err := config.GlobalDir()
			if err != nil {
				return projectView{}, err
			}
			v.Federated, err = project.WorkspaceRoutes(ctx, globalRoot, *a.Workspace)
		} else {
			v.Members, err = project.Members(ctx, a.DB, a.ProjectID, *a.Workspace)
		}
	}
	return v, err
}

func (s appTerminalSource) Checkpoints(ctx context.Context, taskID, cursor string) (checkpoint.ListPage, error) {
	return s.app.Checkpoints.List(ctx, s.app.ProjectID, checkpoint.ListRequest{TaskID: taskID, Cursor: cursor, Limit: terminalPageSize})
}
func (s appTerminalSource) Checkpoint(ctx context.Context, id string) (*checkpoint.Checkpoint, error) {
	return s.app.Checkpoints.LatestSelected(ctx, s.app.ProjectID, checkpoint.Selection{CheckpointID: id})
}
func (s appTerminalSource) Health(ctx context.Context) (readiness.Report, error) {
	// This is the console process, not a claim about a connected MCP process.
	return s.app.Readiness.Report(ctx, readiness.Input{})
}
func (s appTerminalSource) Releases(ctx context.Context) (release.View, error) {
	return s.app.Releases.History(ctx, s.app.ProjectID, "", release.MaxLimit)
}
func (s appTerminalSource) Report(ctx context.Context, id string) (*workitem.BugReport, error) {
	return s.app.WorkItems.Report(ctx, s.app.ProjectID, id, workitem.ReportOptions{})
}

func consoleCommand() *cobra.Command {
	parent := &cobra.Command{Use: "console", Short: "Read versioned console projections for terminal and editor clients"}
	var taskID, cursor, id string
	view := &cobra.Command{Use: "view VIEW", Short: "Read project, checkpoints, checkpoint, health, releases, or report as JSON", Args: cobra.ExactArgs(1), ValidArgs: []string{"project", "checkpoints", "checkpoint", "health", "releases", "report"}, RunE: func(cmd *cobra.Command, args []string) error {
		kind := args[0]
		if kind != "project" && kind != "checkpoints" && kind != "checkpoint" && kind != "health" && kind != "releases" && kind != "report" {
			return fmt.Errorf("unknown console view %q", kind)
		}
		if (kind == "checkpoint" || kind == "report") && id == "" {
			return fmt.Errorf("--id is required for %s", kind)
		}
		if (taskID != "" || cursor != "") && kind != "checkpoints" {
			return fmt.Errorf("--task-id and --cursor apply only to checkpoints")
		}
		if id != "" && kind != "checkpoint" && kind != "report" {
			return fmt.Errorf("--id applies only to checkpoint or report")
		}
		checkout, err := currentCheckout(cmd)
		if err != nil {
			return err
		}
		return withReadOnlyApp(cmd, func(a *app.App) error {
			s := appTerminalSource{app: a, checkout: checkout}
			var data any
			var err error
			switch kind {
			case "project":
				data, err = s.Project(cmd.Context())
			case "checkpoints":
				data, err = s.Checkpoints(cmd.Context(), taskID, cursor)
			case "checkpoint":
				data, err = s.Checkpoint(cmd.Context(), id)
			case "health":
				data, err = s.Health(cmd.Context())
			case "releases":
				data, err = s.Releases(cmd.Context())
			case "report":
				data, err = s.Report(cmd.Context(), id)
			}
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), consoleData{SchemaVersion: 1, ProjectID: a.ProjectID, View: kind, Data: data})
		})
	}}
	view.Flags().StringVar(&taskID, "task-id", "", "checkpoints: exact task filter")
	view.Flags().StringVar(&cursor, "cursor", "", "checkpoints: next-page cursor")
	view.Flags().StringVar(&id, "id", "", "checkpoint/report: selected record ID")
	parent.AddCommand(view)
	return parent
}
