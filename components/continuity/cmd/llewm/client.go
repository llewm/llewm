package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/clientperm"
	"github.com/llewm/llewm/components/continuity/internal/mcpserver"
)

func clientCommand() *cobra.Command {
	parent := &cobra.Command{
		Use:   "client",
		Short: "Set up coding-agent clients to work with Continuity",
	}
	var client, scope, output string
	var write bool
	permissions := &cobra.Command{
		Use:   "permissions",
		Short: "Print the permission rules a client needs for Continuity, or add them to your settings",
		Long:  "Prints the rules that let a client use Continuity without prompts: its MCP tools, the continuity CLI, and the commands of procedures covered by an active standing grant, plus a rule refusing agent runs of `standing-grant create`. An agent cannot grant itself permissions, so run --write yourself at a terminal. For Claude Code, --write merges the rules into the settings file for --scope (user: ~/.claude/settings.json for every project; local: .claude/settings.local.json for this project, personally; project: .claude/settings.json, shared) after backing it up; rerunning adds nothing new. For Codex, --write previews and confirms managed changes to CODEX_HOME/config.toml and rules/llewm.rules; it preserves user entries and backs up changed files. These approvals do not change sandbox filesystem or network access.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			if write && client == clientperm.Codex && scope != "user" {
				return fmt.Errorf("Codex --write supports --scope user only")
			}
			if write && !interactiveInput(cmd) {
				return fmt.Errorf("--write changes your client permissions, so it needs a person at a terminal; run it yourself, not through an agent")
			}
			return withApp(cmd, func(application *app.App) error {
				var tools []string
				if client == clientperm.Codex {
					if tools, err = mcpserver.ToolNames(cmd.Context(), application); err != nil {
						return err
					}
				}
				rules, err := clientperm.Build(cmd.Context(), application.DB, application.ProjectID, client, tools)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				if write {
					if client == clientperm.Codex {
						return writeCodexSettings(cmd, rules)
					}
					home, err := os.UserHomeDir()
					if err != nil {
						return err
					}
					path, err := clientperm.SettingsPath(scope, home, application.Root)
					if err != nil {
						return err
					}
					return writeClaudeSettings(out, path, rules)
				}
				if format == "json" {
					return writeJSON(out, rules)
				}
				return printRules(out, rules)
			})
		},
	}
	permissions.Flags().StringVar(&client, "client", clientperm.ClaudeCode, "claude-code or codex")
	permissions.Flags().BoolVar(&write, "write", false, "apply client permissions yourself at a terminal (with backups)")
	permissions.Flags().StringVar(&scope, "scope", "user", "settings to write with --write: user, local, or project")
	permissions.Flags().StringVar(&output, "output", "text", "text or json")
	parent.AddCommand(permissions, auditHooksCommand())
	return parent
}

func printRules(out io.Writer, rules clientperm.Rules) error {
	if rules.Client == clientperm.ClaudeCode {
		snippet, err := clientperm.ClaudeSnippet(rules)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Claude Code settings (merge into ~/.claude/settings.json, or run this command with --write --scope user):\n\n%s\n", snippet)
	} else {
		fmt.Fprintln(out, "Codex exec-policy rules ($CODEX_HOME/rules/llewm.rules; default ~/.codex):")
		fmt.Fprintln(out)
		for _, value := range append(append([]string{}, rules.Allow...), rules.Deny...) {
			fmt.Fprintln(out, value)
		}
		fmt.Fprintln(out, "\nCodex tool approvals (~/.codex/config.toml):")
		for _, tool := range rules.CodexTools {
			fmt.Fprintf(out, "\n[plugins.%q.mcp_servers.continuity.tools.%s]\napproval_mode = \"approve\"\n", clientperm.CodexPlugin, tool)
		}
	}
	for _, grant := range rules.Grants {
		fmt.Fprintf(out, "\nFrom standing grant %s (%s): %d rules.\n", grant.GrantID, grant.Grant, len(grant.Rules))
	}
	for _, warning := range rules.Warnings {
		fmt.Fprintf(out, "Warning: %s\n", warning)
	}
	if rules.Note != "" {
		fmt.Fprintf(out, "\n%s\n", rules.Note)
	}
	return nil
}

// writeClaudeSettings merges the rules into a Claude Code settings file,
// backing up an existing file first and replacing it atomically.
func writeClaudeSettings(out io.Writer, path string, rules clientperm.Rules) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existed := err == nil
	merged, added, err := clientperm.MergeClaude(data, rules.Allow, rules.Deny)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if len(added) == 0 {
		fmt.Fprintf(out, "%s already has every rule; nothing changed.\n", path)
		return nil
	}
	mode := os.FileMode(0o600)
	if existed {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
		backup := path + ".bak-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.WriteFile(backup, data, mode); err != nil {
			return err
		}
		fmt.Fprintf(out, "Backed up %s to %s.\n", path, backup)
	} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(merged); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	fmt.Fprintf(out, "Added %d rules to %s:\n", len(added), path)
	for _, value := range added {
		fmt.Fprintf(out, "- %s\n", value)
	}
	fmt.Fprintln(out, "New Claude Code sessions use them.")
	if rules.Note != "" {
		fmt.Fprintf(out, "\n%s\n", rules.Note)
	}
	return nil
}

// writeCodexSettings requires an interactive user, enforced before opening the
// project. Only changed lines are printed; unrelated configuration stays private.
func writeCodexSettings(cmd *cobra.Command, rules clientperm.Rules) error {
	homes, err := clientperm.DefaultHomes()
	if err != nil {
		return err
	}
	plan, err := clientperm.PlanCodex(homes.CodexHome, rules)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, rules.Note)
	for _, warning := range rules.Warnings {
		fmt.Fprintln(out, "Warning:", warning)
	}
	for _, name := range plan.Preserved {
		fmt.Fprintln(out, "Preserving user-owned tool settings:", name)
	}
	changed := false
	for _, file := range plan.Files {
		if bytes.Equal(file.Before, file.After) {
			continue
		}
		changed = true
		fmt.Fprintln(out, "\nChanges to", file.Path)
		printPermissionChanges(out, string(file.Before), string(file.After))
	}
	if !changed {
		fmt.Fprintln(out, "Managed permissions are current; nothing changed.")
		return nil
	}
	fmt.Fprint(out, "Apply these read/write approvals? Type yes to continue: ")
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	if strings.TrimSpace(answer) != "yes" {
		fmt.Fprintln(out, "Cancelled; no files changed.")
		return nil
	}
	backups, err := plan.Apply()
	for _, path := range backups {
		fmt.Fprintln(out, "Backup:", path)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Updated managed permissions. Start a new Codex session to use them. Filesystem and network restrictions are unchanged.")
	return nil
}
func printPermissionChanges(out io.Writer, before, after string) {
	lines := func(s string) map[string]int {
		m := map[string]int{}
		for _, line := range strings.Split(s, "\n") {
			m[line]++
		}
		return m
	}
	for _, side := range []struct{ sign, source, other string }{{"-", before, after}, {"+", after, before}} {
		counts := lines(side.other)
		for _, line := range strings.Split(side.source, "\n") {
			if counts[line] > 0 {
				counts[line]--
				continue
			}
			if line != "" && !strings.HasPrefix(line, "# BEGIN continuity managed") && !strings.HasPrefix(line, "# END continuity managed") {
				fmt.Fprintln(out, side.sign, line)
			}
		}
	}
}
