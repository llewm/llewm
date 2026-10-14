package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/clientinstall"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/spf13/cobra"
)

func installCommand() *cobra.Command {
	var client, output string
	var dryRun, permissions bool
	cmd := &cobra.Command{Use: "install", Short: "Install the bundled Llewm plugin for a coding-agent client", Long: "Install the plugin built with this executable at user scope, using the client's own plugin manager. Preview with --dry-run. Run installation yourself at a terminal. --permissions invokes the existing reviewed permission setup after plugin installation; initialize the current Git project first. Hook trust and fresh-session delivery are verified separately. This command never installs the executable, initializes a project, migrates a database, or changes memory sync.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		if client != "codex" && client != "claude-code" {
			return fmt.Errorf("--client must be codex or claude-code")
		}
		if !dryRun && !interactiveInput(cmd) {
			return fmt.Errorf("install changes client setup, so run it yourself at a terminal; agents can use --dry-run")
		}
		root, err := config.GlobalDir()
		if err != nil {
			return err
		}
		pkg, err := clientinstall.Bundled()
		if err != nil {
			return err
		}
		r := clientinstall.Receipt{Client: client, Scope: "user", Directory: filepath.Join(root, "plugins", "installer", "marketplace"), BundleSHA256: pkg.Digest, PluginVersion: pkg.Version, State: "blocked", Permissions: "needs_review", HookTrust: "needs_client_review", RestartRequired: true}
		r.Executable = buildinfo.Current()
		// Hooks and MCP use PATH, so refuse a shadowed or unavailable executable
		// rather than installing a package that would launch a different build.
		current, err := os.Executable()
		if err != nil {
			return err
		}
		path, err := exec.LookPath("llewm")
		if err == nil {
			var a, b os.FileInfo
			a, err = os.Stat(current)
			if err == nil {
				b, err = os.Stat(path)
				if err == nil && !os.SameFile(a, b) {
					err = fmt.Errorf("PATH resolves llewm to %s instead of this executable %s", path, current)
				}
			}
		}
		if err == nil {
			program := client
			if client == "claude-code" {
				program = "claude"
			}
			var executable string
			executable, err = exec.LookPath(program)
			if err == nil {
				service := clientinstall.Service{Client: client, Root: r.Directory, Package: pkg, Run: clientinstall.NativeRunner(executable)}
				if dryRun {
					r, err = service.Inspect(cmd.Context())
				} else {
					r, err = service.Apply(cmd.Context())
				}
			}
		}
		if err != nil {
			r.Problem = err.Error()
			if r.State == "preview" {
				r.State = "blocked"
			}
		}
		if err == nil && !dryRun && permissions {
			setup := newRootCommand("llewm")
			setup.SetIn(cmd.InOrStdin())
			setup.SetOut(cmd.OutOrStdout())
			if format == "json" {
				setup.SetOut(cmd.ErrOrStderr())
			}
			setup.SetErr(cmd.ErrOrStderr())
			setup.SetContext(cmd.Context())
			args := []string{"client", "permissions", "--client", client, "--write", "--scope", "user"}
			projectRoot, _ := cmd.Flags().GetString("project-root")
			if projectRoot != "" {
				args = append([]string{"--project-root", projectRoot}, args...)
			}
			setup.SetArgs(args)
			err = setup.Execute()
			if err == nil {
				permissionCmd, _, findErr := setup.Find([]string{"client", "permissions"})
				if findErr != nil {
					return findErr
				}
				r.Permissions = permissionCmd.Annotations["llewm_permission_result"]
				if r.Permissions == "" {
					r.Permissions = "needs_review"
				}
			} else {
				r.State = "partial"
				r.Problem = fmt.Sprintf("Plugin installed; permission setup remains: %v", err)
			}
		}
		if format == "json" {
			if writeErr := writeJSON(cmd.OutOrStdout(), r); writeErr != nil {
				return writeErr
			}
		} else {
			clientinstall.WriteText(cmd.OutOrStdout(), r)
		}
		return err
	}}
	cmd.Flags().StringVar(&client, "client", "", "codex or claude-code (required)")
	_ = cmd.MarkFlagRequired("client")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "inspect and preview without writing packages or client configuration")
	cmd.Flags().BoolVar(&permissions, "permissions", false, "also run terminal-reviewed client permission setup in an initialized Git project")
	cmd.Flags().StringVar(&output, "output", "text", "text or json (permission review still uses interactive text)")
	return cmd
}
