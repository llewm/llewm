package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/clientperm"
	"github.com/llewm/llewm/components/continuity/internal/standinggrant"
)

// interactiveInput reports whether the command reads from a terminal. Creating
// a standing grant needs one, so an agent's non-interactive shell cannot grant
// itself permission. Tests replace it.
var interactiveInput = func(cmd *cobra.Command) bool {
	file, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

// parseExpiry accepts a duration (720h, or 30d for days) or an RFC 3339 time
// or date. An empty value means no expiry.
func parseExpiry(value string, now time.Time) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		if count, err := strconv.Atoi(days); err == nil && count > 0 {
			at := now.AddDate(0, 0, count)
			return &at, nil
		}
	}
	if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
		at := now.Add(duration)
		return &at, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if at, err := time.Parse(layout, value); err == nil {
			at = at.UTC()
			return &at, nil
		}
	}
	return nil, fmt.Errorf("expiry %q must be a duration (720h, 30d) or an RFC 3339 time or date", value)
}

func grantWhen(value *time.Time) string {
	if value == nil {
		return "no expiry"
	}
	return "until " + value.UTC().Format("2006-01-02 15:04Z")
}

func writeGrantLine(out io.Writer, grant standinggrant.Grant) {
	state := grant.State
	if grant.VoidReason != "" && grant.State == standinggrant.Void {
		state += " (" + grant.VoidReason + ")"
	}
	fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\tby %s\n", grant.ID, grant.Ref(), grant.Trigger, state, grantWhen(grant.ExpiresAt), grant.Creator)
}

func writeGrant(out io.Writer, grant standinggrant.Grant) {
	fmt.Fprintf(out, "Standing grant: %s\nGrant: %s on %s\nState: %s", grant.ID, grant.Ref(), grant.Trigger, grant.State)
	if grant.VoidReason != "" {
		fmt.Fprintf(out, " (%s)", grant.VoidReason)
	}
	fmt.Fprintf(out, "\nExpiry: %s\nCreator: %s (%s)\n", grantWhen(grant.ExpiresAt), grant.Creator, grant.Authority)
	if grant.Reason != "" {
		fmt.Fprintf(out, "Reason: %s\n", grant.Reason)
	}
	fmt.Fprintln(out, "Covers:")
	for _, covered := range grant.Covers {
		fmt.Fprintf(out, "- procedure:%s@%d", covered.ProcedureID, covered.Revision)
		if covered.Title != "" {
			fmt.Fprintf(out, ": %s", covered.Title)
		}
		if covered.LatestRevision != 0 {
			fmt.Fprintf(out, " (now at revision %d)", covered.LatestRevision)
		}
		fmt.Fprintln(out)
	}
	if len(grant.Parameters) > 0 {
		fmt.Fprintln(out, "Parameters:")
		for _, name := range sortedKeys(grant.Parameters) {
			fmt.Fprintf(out, "- %s=%s\n", name, grant.Parameters[name])
		}
	}
	if grant.Revocation != nil {
		fmt.Fprintf(out, "Revoked: %s by %s", grant.Revocation.RecordedAt.UTC().Format(time.RFC3339), grant.Revocation.Creator)
		if grant.Revocation.Reason != "" {
			fmt.Fprintf(out, ": %s", grant.Revocation.Reason)
		}
		fmt.Fprintln(out)
	}
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func defaultCreator() string {
	if current, err := user.Current(); err == nil {
		return current.Username
	}
	return ""
}

func standingGrantCommand() *cobra.Command {
	parent := &cobra.Command{
		Use:   "standing-grant",
		Short: "Let agents run a procedure on a trigger without asking, until you revoke it",
		Long: "A standing grant is your permission, recorded on this machine, for agents to run a procedure revision when a trigger occurs (after_slice_commit: after a commit that completes a slice with passing checks). It covers the procedure and every procedure its steps name as procedure:ID, at their current revisions; a new revision of any of them voids it. Only a person at a terminal can create one. Continuity executes nothing; the session bootstrap lists active grants.\n\n" +
			"Agents must verify the accepted revision with `llewm procedure show PROCEDURE` before recommending a grant. They can inspect creation flags without invoking the denied create command by running `continuity help standing-grant create`.\n\n" +
			"Before execution, read `llewm standing-grant show GRANT_ID --commands` and preserve bound parameters and individual steps. In an escalation, cite the active grant ID, covered procedure revision, satisfied trigger, exact target, payload identity and intended effect. Harness permission is separate from the grant: a denial does not revoke it. Record the denial in a work narrative, do not route around it, and request only the specific confirmation required by the harness. See docs/resources-and-runbooks.md#harness-denials.",
	}
	var output, trigger, expires, creator, reason string
	var params []string
	var all bool
	outputFlags := func(command *cobra.Command) {
		command.Flags().StringVar(&output, "output", "text", "text or json")
	}
	create := &cobra.Command{
		Use:   "create PROCEDURE[@REVISION]",
		Short: "Grant a procedure revision on a trigger (asks for confirmation at a terminal)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			if !interactiveInput(cmd) {
				return fmt.Errorf("creating a standing grant needs a person at a terminal; run this command yourself in a terminal, not through an agent")
			}
			id, revisionText, _ := strings.Cut(strings.TrimPrefix(args[0], "procedure:"), "@")
			revision := 0
			if revisionText != "" {
				if revision, err = strconv.Atoi(revisionText); err != nil || revision < 1 {
					return fmt.Errorf("procedure revision %q must be a positive integer", revisionText)
				}
			}
			expiry, err := parseExpiry(expires, time.Now().UTC())
			if err != nil {
				return err
			}
			parameters := map[string]string{}
			for _, param := range params {
				name, value, ok := strings.Cut(param, "=")
				if !ok || strings.TrimSpace(name) == "" {
					return fmt.Errorf("--param %q must be NAME=VALUE", param)
				}
				parameters[strings.TrimSpace(name)] = value
			}
			return withApp(cmd, func(application *app.App) error {
				procedure, err := application.Procedures.Get(cmd.Context(), application.ProjectID, id, revision)
				if err != nil {
					return err
				}
				if procedure == nil {
					return fmt.Errorf("procedure:%s has no accepted revision; list accepted procedures with `llewm procedure show --status accepted`, and inspect grant flags with `continuity help standing-grant create`", id)
				}
				covers := []string{fmt.Sprintf("procedure:%s@%d", procedure.ID, procedure.Revision)}
				for _, named := range standinggrant.NamedProcedures(*procedure) {
					covers = append(covers, "procedure:"+named)
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "Grant agents permission to run %s (%s) %s, %s, without asking?\nCovers: %s\n", covers[0], procedure.Title, strings.ReplaceAll(trigger, "_", " "), grantWhen(expiry), strings.Join(covers, ", "))
				for _, effect := range procedure.DeclaredEffects {
					fmt.Fprintf(out, "Effect: %s\n", effect)
				}
				fmt.Fprint(out, "Type yes to grant: ")
				answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if strings.TrimSpace(answer) != "yes" {
					return fmt.Errorf("not granted")
				}
				grant, err := application.StandingGrants.Create(cmd.Context(), standinggrant.Request{ProjectID: application.ProjectID, ProcedureID: procedure.ID, Revision: procedure.Revision, Trigger: trigger, Parameters: parameters, ExpiresAt: expiry, Creator: creator, Reason: reason})
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(out, grant)
				}
				writeGrant(out, grant)
				return nil
			})
		},
	}
	create.Flags().StringVar(&trigger, "trigger", standinggrant.TriggerAfterSliceCommit, "when agents may run it: "+strings.Join(standinggrant.Triggers, ", "))
	create.Flags().StringVar(&expires, "expires", "", "expiry as a duration (720h, 30d) or an RFC 3339 time or date; default none")
	create.Flags().StringArrayVar(&params, "param", nil, "NAME=VALUE for a parameter the procedures declare (repeatable)")
	create.Flags().StringVar(&creator, "creator", defaultCreator(), "who grants it")
	create.Flags().StringVar(&reason, "reason", "", "why agents may run it without asking")
	outputFlags(create)
	revoke := &cobra.Command{
		Use:   "revoke ID",
		Short: "End a standing grant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				grant, err := application.StandingGrants.Revoke(cmd.Context(), application.ProjectID, args[0], creator, reason)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), grant)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Revoked %s (%s).\n", grant.ID, grant.Ref())
				return nil
			})
		},
	}
	revoke.Flags().StringVar(&creator, "creator", defaultCreator(), "who revokes it")
	revoke.Flags().StringVar(&reason, "reason", "", "why")
	outputFlags(revoke)
	list := &cobra.Command{
		Use:   "list",
		Short: "List active standing grants, or with --all every grant and its state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				grants, err := application.StandingGrants.List(cmd.Context(), application.ProjectID, all)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), grants)
				}
				for _, grant := range grants {
					writeGrantLine(cmd.OutOrStdout(), grant)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Standing grants: %d. %s\n", len(grants), standinggrant.Disclaimer)
				return nil
			})
		},
	}
	list.Flags().BoolVar(&all, "all", false, "include expired, revoked, and void grants")
	outputFlags(list)
	var commands bool
	var codexRules []string
	show := &cobra.Command{
		Use:   "show ID",
		Short: "Show one standing grant, what it covers, and its state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(codexRules) > 0 && !commands {
				return fmt.Errorf("--codex-rules requires --commands")
			}
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				if commands {
					preview, err := clientperm.Preview(cmd.Context(), application.DB, application.ProjectID, args[0], codexRules)
					if err != nil {
						return err
					}
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), preview)
					}
					writeGrant(cmd.OutOrStdout(), preview.Grant)
					fmt.Fprintln(cmd.OutOrStdout(), preview.Disclaimer)
					for _, procedure := range preview.Procedures {
						fmt.Fprintf(cmd.OutOrStdout(), "\nprocedure:%s@%d (working directory: %s)\n", procedure.ID, procedure.Revision, procedure.WorkingDirectory)
						for _, step := range procedure.Steps {
							fmt.Fprintf(cmd.OutOrStdout(), "%s [%s]: %s\n", step.ID, step.State, step.Description)
							if len(step.Missing) > 0 {
								fmt.Fprintf(cmd.OutOrStdout(), "  Missing parameters: %s\n", strings.Join(step.Missing, ", "))
							}
							if step.Command != "" {
								fmt.Fprintf(cmd.OutOrStdout(), "  Command: %s\n", step.Command)
							}
							if step.CheckCommand != "" {
								fmt.Fprintf(cmd.OutOrStdout(), "  Rule check (not run): %s\n", step.CheckCommand)
							}
						}
					}
					return nil
				}
				grant, err := application.StandingGrants.Get(cmd.Context(), application.ProjectID, args[0])
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), grant)
				}
				writeGrant(cmd.OutOrStdout(), grant)
				return nil
			})
		},
	}
	show.Flags().BoolVar(&commands, "commands", false, "preview bound commands from covered revisions without executing them")
	show.Flags().StringArrayVar(&codexRules, "codex-rules", nil, "with --commands, print inert Codex check commands for these rule files (repeatable; does not run checks)")
	outputFlags(show)
	parent.AddCommand(create, revoke, list, show)
	return parent
}
