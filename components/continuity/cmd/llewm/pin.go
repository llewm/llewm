package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/pin"
)

// parsePinScope turns "project" or "KIND:ID" into a scope kind and ID.
func parsePinScope(scope string) (string, string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" || scope == "project" {
		return "project", "", nil
	}
	return pin.ParseRef(scope)
}

func pinCommand() *cobra.Command {
	var scope, creator, authority, task, output string
	var unpin bool
	command := &cobra.Command{
		Use:   "pin [TARGET]",
		Short: "Pin a resource, procedure, decision, or topic to a scope, or list current pins",
		Long:  "With TARGET (resource:ID, procedure:ID, decision:ID, or topic:ID), pin it to --scope (project, topic:ID, roadmap_node:ID, or task:ID), or remove that pin with --unpin. Without TARGET, list current pins. Pins only surface records in the bootstrap; they change nothing else.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				if len(args) == 0 {
					lines, err := pin.Current(cmd.Context(), application.DB, application.ProjectID)
					if task != "" {
						lines, err = pin.ForTask(cmd.Context(), application.DB, application.ProjectID, task)
					}
					if err != nil {
						return err
					}
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), lines)
					}
					for _, line := range lines {
						fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tscope=%s\n", line.Ref, singleLineText(line.Title), line.Scope)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Pins: %d. %s\n", len(lines), pin.Disclaimer)
					return nil
				}
				targetKind, targetID, err := pin.ParseRef(args[0])
				if err != nil {
					return err
				}
				scopeKind, scopeID, err := parsePinScope(scope)
				if err != nil {
					return err
				}
				action := "pin"
				if unpin {
					action = "unpin"
				}
				value, err := application.Pins.Set(cmd.Context(), pin.Pin{ProjectID: application.ProjectID, TargetKind: targetKind, TargetID: targetID, ScopeKind: scopeKind, ScopeID: scopeID, Action: action, Creator: creator, Authority: authority})
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), value)
			})
		},
	}
	command.Flags().StringVar(&scope, "scope", "project", "project, topic:ID, roadmap_node:ID, or task:ID")
	command.Flags().BoolVar(&unpin, "unpin", false, "remove the pin for this target and scope")
	command.Flags().StringVar(&creator, "creator", "", "creator identity (required to pin or unpin)")
	command.Flags().StringVar(&authority, "authority", "", "caller_asserted or user_reported")
	command.Flags().StringVar(&task, "task", "", "list only the pins the bootstrap would show for this task")
	command.Flags().StringVar(&output, "output", "text", "text or json (listing)")
	return command
}
