package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/workspacechange"
)

type projectView struct {
	SchemaVersion int                     `json:"schema_version"`
	ProjectID     string                  `json:"project_id"`
	Name          string                  `json:"name"`
	Root          string                  `json:"root"`
	Checkout      project.Checkout        `json:"checkout"`
	Identity      project.Resolution      `json:"identity"`
	Database      string                  `json:"database"`
	StoreInside   bool                    `json:"store_inside_root"`
	Locations     []project.Location      `json:"locations"`
	Workspace     *project.Workspace      `json:"workspace,omitempty"`
	Members       []project.MemberState   `json:"members,omitempty"`
	Federated     []project.RegistryEntry `json:"federated_members,omitempty"`
}

func projectCommand() *cobra.Command {
	parent := &cobra.Command{Use: "project", Short: "Inspect project identity and explicitly rebind a moved checkout"}

	var showOutput string
	var limit int
	show := &cobra.Command{Use: "show", Short: "Show the project, checkout, and recorded repository locations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(showOutput)
		if err != nil {
			return err
		}
		if limit < 1 || limit > 64 {
			return fmt.Errorf("limit must be between 1 and 64")
		}
		checkout, err := currentCheckout(cmd)
		if err != nil {
			return err
		}
		return withReadOnlyApp(cmd, func(application *app.App) error {
			view, err := readProjectView(cmd.Context(), application, checkout, limit)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), view)
			}
			writeProjectText(cmd, view)
			return nil
		})
	}}
	show.Flags().IntVar(&limit, "limit", 10, "maximum location observations (1-64)")
	show.Flags().StringVar(&showOutput, "output", "text", "text or json")
	var createNew bool
	rebind := &cobra.Command{
		Use:   "rebind [PROJECT_ID]",
		Short: "Bind this checkout to an existing project after a move, or to a new project with --new",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if createNew == (len(args) == 1) {
				return fmt.Errorf("pass exactly one of PROJECT_ID or --new")
			}
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			db, err := database.Open(cmd.Context(), cfg.Storage.Database)
			if err != nil {
				return err
			}
			defer db.Close()
			projectID := ""
			if len(args) == 1 {
				projectID = args[0]
			}
			result, err := project.Resolver{DB: db}.Rebind(cmd.Context(), root, cfg.Project.Name, projectID)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	rebind.Flags().BoolVar(&createNew, "new", false, "create a separate project for this checkout")

	parent.AddCommand(show, rebind)
	return parent
}

func workspaceCommand() *cobra.Command {
	parent := &cobra.Command{Use: "workspace", Short: "Group several repositories under one folder into a workspace"}
	var showOutput string
	show := &cobra.Command{
		Use:   "show",
		Short: "Show the workspace and machine-local routes for its members",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := workspaceRoot(cmd)
			if err != nil {
				return err
			}
			workspace, err := project.LoadWorkspace(dir)
			if err != nil {
				return err
			}
			if workspace == nil {
				return fmt.Errorf("%s has no workspace manifest", dir)
			}
			format, err := outputFormat(showOutput)
			if err != nil {
				return err
			}
			globalRoot, err := config.GlobalDir()
			if err != nil {
				return err
			}
			routes, err := project.WorkspaceRoutes(cmd.Context(), globalRoot, *workspace)
			if err != nil {
				return err
			}
			view := struct {
				SchemaVersion int                     `json:"schema_version"`
				Workspace     *project.Workspace      `json:"workspace"`
				Routes        []project.RegistryEntry `json:"routes"`
			}{1, workspace, routes}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), view)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Workspace: %s (%s)\nRoot: %s\n", workspace.Name, workspace.Mode, workspace.Root)
			byMember := make(map[string]project.RegistryEntry, len(routes))
			for _, route := range routes {
				byMember[route.Member] = route
			}
			for _, member := range workspace.Members {
				route, ok := byMember[member.Path]
				if !ok {
					fmt.Fprintf(cmd.OutOrStdout(), "  %-24s not registered on this machine\n", member.Path)
					continue
				}
				fmt.Fprintln(cmd.OutOrStdout(), routeLine(route))
			}
			return nil
		},
	}
	show.Flags().StringVar(&showOutput, "output", "text", "text or json")
	var routeOutput string
	route := &cobra.Command{
		Use:   "route REFERENCE",
		Short: "Resolve a federated member, repository, project, lineage, or fingerprint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workspaceRoot(cmd)
			if err != nil {
				return err
			}
			workspace, err := project.LoadWorkspace(dir)
			if err != nil {
				return err
			}
			if workspace == nil || workspace.Mode != project.ModeFederated {
				return fmt.Errorf("%s is not a federated workspace", dir)
			}
			format, err := outputFormat(routeOutput)
			if err != nil {
				return err
			}
			globalRoot, err := config.GlobalDir()
			if err != nil {
				return err
			}
			resolved, err := project.ResolveRegistryReference(cmd.Context(), globalRoot, *workspace, args[0])
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), resolved)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s -> project=%s repository=%s lineage=%s root=%s store=%s\n", args[0], resolved.ProjectID, resolved.RepositoryID, resolved.LineageID, resolved.Root, resolved.Store)
			return nil
		},
	}
	route.Flags().StringVar(&routeOutput, "output", "text", "text or json")
	var name, mode string
	var write bool
	initialize := &cobra.Command{
		Use:   "init",
		Short: "Propose a unified or federated workspace manifest; --write saves it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := workspaceRoot(cmd)
			if err != nil {
				return err
			}
			if existing, err := project.LoadWorkspace(dir); err != nil || existing != nil {
				if err == nil {
					err = fmt.Errorf("%s already exists; edit it directly", project.WorkspacePath(dir))
				}
				return err
			}
			proposal, warnings, err := project.ProposeWorkspace(dir, name, mode)
			if err != nil {
				return err
			}
			content, err := proposal.Encode()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, warning := range warnings {
				fmt.Fprintf(out, "Warning: %s\n", warning)
			}
			if !write {
				fmt.Fprintf(out, "Proposed %s:\n\n%s\nNothing written. Review the members, then rerun with --write to save it.\n", project.WorkspacePath(dir), content)
				return nil
			}
			path, err := project.WriteWorkspace(proposal)
			if err != nil {
				return err
			}
			if proposal.Mode == project.ModeFederated {
				fmt.Fprintf(out, "Wrote %s:\n\n%s\nRun `llewm init` inside each member to register its independent project and store.\n", path, content)
			} else {
				fmt.Fprintf(out, "Wrote %s:\n\n%s\nRun `llewm init` here to create the workspace store.\n", path, content)
			}
			return nil
		},
	}
	initialize.Flags().StringVar(&name, "name", "", "workspace name (default: the folder name)")
	initialize.Flags().StringVar(&mode, "mode", project.ModeUnified, "workspace mode: unified or federated")
	initialize.Flags().BoolVar(&write, "write", false, "save the proposed manifest")

	var changeMode, changeConfirm, changeOutput string
	var changeApply bool
	change := &cobra.Command{
		Use:   "change",
		Short: "Preview or apply a reviewed unified/federated topology change",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := workspaceRoot(cmd)
			if err != nil {
				return err
			}
			format, err := outputFormat(changeOutput)
			if err != nil {
				return err
			}
			if changeApply {
				result, err := workspacechange.Apply(cmd.Context(), dir, changeMode, changeConfirm)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Changed workspace from %s to %s after %d portable imports (%d records appended).\nManifest: %s\n", result.Plan.FromMode, result.Mode, result.ImportsApplied, result.RecordsAppended, result.ManifestPath)
				fmt.Fprintln(cmd.OutOrStdout(), "Previous stores remain in place; raw events, sessions, and usage samples were not copied by the curated portable profile.")
				return nil
			}
			plan, err := workspacechange.BuildPlan(cmd.Context(), dir, changeMode)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), plan)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Proposed workspace %s: %s -> %s\nLineage: %s\n", plan.Operation, plan.FromMode, plan.ToMode, plan.LineageID)
			for _, member := range plan.Members {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %-18s store=%s", member.Path, member.Action, member.Store)
				if member.LineageID != "" {
					fmt.Fprintf(cmd.OutOrStdout(), " lineage=%s", member.LineageID)
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			for _, blocker := range plan.Blockers {
				fmt.Fprintln(cmd.OutOrStdout(), "Blocked:", blocker)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Nothing written. Curated memory moves through validated portable export/import; raw events, sessions, and usage samples stay in their original stores.")
			if plan.ApplyAllowed {
				fmt.Fprintf(cmd.OutOrStdout(), "After review, rerun with --apply --confirm %s\n", plan.Confirmation)
			}
			return nil
		},
	}
	change.Flags().StringVar(&changeMode, "mode", "", "target mode: unified or federated (required)")
	change.Flags().BoolVar(&changeApply, "apply", false, "apply the reviewed plan")
	change.Flags().StringVar(&changeConfirm, "confirm", "", "exact plan confirmation digest (required with --apply)")
	change.Flags().StringVar(&changeOutput, "output", "text", "text or json")
	_ = change.MarkFlagRequired("mode")

	var editorPath, editorOutput string
	var editorWrite bool
	editor := &cobra.Command{
		Use:   "editor",
		Short: "Generate a non-authoritative VS Code workspace from the manifest",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := workspaceRoot(cmd)
			if err != nil {
				return err
			}
			format, err := outputFormat(editorOutput)
			if err != nil {
				return err
			}
			workspace, err := project.LoadWorkspace(dir)
			if err != nil {
				return err
			}
			if workspace == nil {
				return fmt.Errorf("%s has no workspace manifest", dir)
			}
			folders := make([]struct {
				Path string `json:"path"`
			}, 0, len(workspace.Members))
			for _, member := range workspace.Members {
				folders = append(folders, struct {
					Path string `json:"path"`
				}{Path: filepath.ToSlash(member.Path)})
			}
			content, err := json.MarshalIndent(struct {
				Folders  any            `json:"folders"`
				Settings map[string]any `json:"settings"`
			}{Folders: folders, Settings: map[string]any{}}, "", "  ")
			if err != nil {
				return err
			}
			content = append(content, '\n')
			if !editorWrite {
				_, err = cmd.OutOrStdout().Write(content)
				return err
			}
			if strings.TrimSpace(editorPath) == "" {
				editorPath = filepath.Join(dir, filepath.Base(dir)+".code-workspace")
			} else if !filepath.IsAbs(editorPath) {
				editorPath = filepath.Join(dir, editorPath)
			}
			file, err := os.OpenFile(editorPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			if _, err = file.Write(content); err == nil {
				err = file.Sync()
			}
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"path": editorPath, "folders": len(folders), "authoritative": false})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s with %d folders. The workspace manifest remains authoritative.\n", editorPath, len(folders))
			return nil
		},
	}
	editor.Flags().BoolVar(&editorWrite, "write", false, "write a new .code-workspace file instead of printing it")
	editor.Flags().StringVar(&editorPath, "path", "", "output path (default: WORKSPACE/WORKSPACE.code-workspace)")
	editor.Flags().StringVar(&editorOutput, "output", "text", "text or json for the write receipt")

	parent.AddCommand(show, route, initialize, change, editor)
	return parent
}

// routeLine renders one federated member route for text output.
func routeLine(route project.RegistryEntry) string {
	lineage := route.LineageID
	if lineage == "" {
		lineage = "lineage not initialized"
	}
	line := fmt.Sprintf("  %-24s %s  %s  store=%s", route.Member, route.ProjectID, lineage, route.Store)
	if route.Missing {
		line += "  (checkout missing at " + route.Root + ")"
	}
	return line
}

func workspaceRoot(cmd *cobra.Command) (string, error) {
	dir, err := cmd.Root().PersistentFlags().GetString("project-root")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(dir) == "" {
		if dir, err = os.Getwd(); err != nil {
			return "", err
		}
	}
	return filepath.Abs(dir)
}

func currentCheckout(cmd *cobra.Command) (project.Checkout, error) {
	start, err := cmd.Root().PersistentFlags().GetString("project-root")
	if err != nil {
		return project.Checkout{}, err
	}
	if strings.TrimSpace(start) == "" {
		if start, err = os.Getwd(); err != nil {
			return project.Checkout{}, err
		}
	}
	return project.Resolve(start)
}

func writeProjectText(cmd *cobra.Command, view projectView) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Project: %s (%s)\n", view.ProjectID, view.Name)
	fmt.Fprintf(out, "Root: %s\n", view.Root)
	switch {
	case view.Checkout.Member != "":
		fmt.Fprintf(out, "Checkout: %s (%s, member %s)\n", view.Checkout.Path, view.Checkout.Kind, view.Checkout.Member)
	case view.Checkout.Path != view.Root:
		fmt.Fprintf(out, "Checkout: %s (%s)\n", view.Checkout.Path, view.Checkout.Kind)
	default:
		fmt.Fprintf(out, "Checkout: %s\n", view.Checkout.Kind)
	}
	if view.Workspace != nil {
		memberCount := len(view.Members)
		if view.Workspace.Mode == project.ModeFederated {
			memberCount = len(view.Workspace.Members)
		}
		fmt.Fprintf(out, "Workspace: %s (%s, %d members)\n", view.Workspace.Name, view.Workspace.Mode, memberCount)
		for _, member := range view.Members {
			state := "not recorded yet"
			switch {
			case !member.Present:
				state = "missing checkout"
			case member.RepositoryID != "":
				state = member.RepositoryID
				if member.RootCommit != "" && len(member.RootCommit) > 12 {
					state += " root=" + member.RootCommit[:12]
				}
			}
			fmt.Fprintf(out, "  %-24s %s\n", member.Path, state)
		}
		for _, route := range view.Federated {
			fmt.Fprintln(out, routeLine(route))
		}
	}
	fmt.Fprintf(out, "Store: %s (inside root: %t)\n", view.Database, view.StoreInside)
	if view.Identity.Outcome == project.OutcomeRelocationPending {
		fmt.Fprintf(out, "Relocation: pending from %s; the next write records it\n", view.Identity.PreviousRoot)
	}
	fmt.Fprintf(out, "Locations: %d shown, newest first\n", len(view.Locations))
	for _, location := range view.Locations {
		commit := location.RootCommit
		if commit == "" {
			commit = "unknown"
		} else if len(commit) > 12 {
			commit = commit[:12]
		}
		fmt.Fprintf(out, "  %s  %-9s root=%s  %s\n", location.ObservedAt, location.Source, commit, location.Path)
	}
}
