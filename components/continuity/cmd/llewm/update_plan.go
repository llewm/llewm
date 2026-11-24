package main

import (
	"context"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/clientinstall"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/diagnostic"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/updatecheck"
	"github.com/spf13/cobra"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type storeUpdate struct {
	Database         database.Status `json:"database"`
	Roots            []string        `json:"roots"`
	TargetMigration  string          `json:"target_migration,omitempty"`
	MigrationCommand string          `json:"migration_command,omitempty"`
	WriterFloor      *int            `json:"writer_floor,omitempty"`
	WriterImpact     string          `json:"writer_impact"`
	Problem          string          `json:"problem,omitempty"`
}
type machineUpdate struct {
	SchemaVersion         int                     `json:"schema_version"`
	Target                updatecheck.Report      `json:"target"`
	TargetError           string                  `json:"target_error,omitempty"`
	RegistryError         string                  `json:"registry_error,omitempty"`
	Stores                []storeUpdate           `json:"stores"`
	Plugins               []clientinstall.Receipt `json:"plugins"`
	PluginComparisonBasis string                  `json:"plugin_comparison_basis"`
	RuntimeState          string                  `json:"runtime_state"`
	RestartGuidance       string                  `json:"restart_guidance"`
}

func storeUpdates(ctx context.Context, entries []project.RegistryEntry, target updatecheck.Report) []storeUpdate {
	result := []storeUpdate{}
	indices := map[string]int{}
	for _, entry := range entries {
		path := filepath.Clean(entry.Store)
		if real, e := filepath.EvalSymlinks(path); e == nil {
			path = real
		}
		key := path
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if i, ok := indices[key]; ok {
			result[i].Roots = append(result[i].Roots, entry.Root)
			continue
		}
		indices[key] = len(result)
		result = append(result, storeUpdate{Database: database.Status{Path: path}, Roots: []string{entry.Root}, TargetMigration: target.TargetMigration, WriterImpact: "unknown"})
	}
	for i := range result {
		r := &result[i]
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		status, err := database.Inspect(bounded, r.Database.Path)
		r.Database = status
		if err != nil {
			r.Problem = diagnostic.Bounded(err.Error())
			cancel()
			continue
		}
		if db, e := database.OpenForMigrationInspection(bounded, status.Path, status.AppliedMigration); e == nil {
			if floor, e := database.WriterFloor(bounded, db); e == nil {
				r.WriterFloor = &floor
				if target.TargetWriterAPI != nil {
					switch {
					case *target.TargetWriterAPI < floor:
						r.WriterImpact = "target_below_floor_writes_refused"
					case *target.TargetWriterAPI > floor:
						r.WriterImpact = "floor_would_rise_older_writers_defer_checkpoints"
					default:
						r.WriterImpact = "unchanged"
					}
				}
			}
			db.Close()
		}
		cancel()
		if target.TargetMigration == "" || status.AppliedMigration == target.TargetMigration {
			continue
		}
		if status.State != "pending" && status.State != "current" {
			r.Problem = "Schema is ahead or inconsistent with this executable; inspect with the qualified target before planning migration."
			continue
		}
		if target.TargetMigration < status.AppliedMigration {
			r.Problem = "Target migration precedes this store; automatic downgrade is unsupported."
			continue
		}
		// The CLI discovers storage through the checkout config. Never print a
		// command that could migrate a different store than this reviewed route.
		for _, root := range r.Roots {
			if project.PathAvailability(root) != "present" {
				continue
			}
			cfg, e := config.Load(root)
			if e != nil {
				continue
			}
			configured := filepath.Clean(cfg.Storage.Database)
			if real, e := filepath.EvalSymlinks(configured); e == nil {
				configured = real
			}
			match := configured == status.Path
			if runtime.GOOS == "windows" {
				match = strings.EqualFold(configured, status.Path)
			}
			if !match {
				continue
			}
			backup, e := database.NextBackupPath(cfg.Backups.Dir, time.Now())
			if e != nil {
				continue
			}
			r.MigrationCommand = commandHint("--project-root", root, "database", "migrate", "--from", status.AppliedMigration, "--to", target.TargetMigration, "--backup", backup)
			break
		}
		if r.MigrationCommand == "" {
			r.Problem = "No reachable registered checkout currently routes to this store; restore or explicitly rebind its configuration before migrating."
		}
	}
	return result
}

func updatePlanCommand() *cobra.Command {
	var source, manifest, artifact, target, output string
	cmd := &cobra.Command{Use: "plan", Short: "Read machine update requirements without installing, migrating, or contacting remotes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		if source != "" && manifest != "" {
			return fmt.Errorf("choose source or manifest, not both")
		}
		if artifact != "" && manifest == "" {
			return fmt.Errorf("--artifact requires --manifest")
		}
		global, err := config.GlobalDir()
		if err != nil {
			return err
		}
		if source == "" && manifest == "" {
			settings, e := updatecheck.LoadAutomaticConfig(global)
			if e == nil {
				source = settings.SourceRoot
			}
		}
		report := machineUpdate{SchemaVersion: 1, Stores: []storeUpdate{}, Plugins: []clientinstall.Receipt{}, PluginComparisonBasis: "bundled_files_of_this_executable; refresh and recheck with the qualified target after installation", RuntimeState: "unknown", RestartGuidance: "No live MCP identities were observed. After binary or plugin refresh, compare continuity_info in each client with llewm version; restart stale clients. Writer impact describes the store floor, not an observed running process."}
		if source != "" {
			bounded, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			report.Target, err = updatecheck.Source(bounded, buildinfo.Current(), source, "origin", "main", false)
			cancel()
		} else if manifest != "" {
			report.Target, err = updatecheck.Release(buildinfo.Current(), manifest, target, artifact)
		} else {
			migration, _ := database.CompiledLatestMigration()
			writer := buildinfo.WriterAPIVersion
			installed := buildinfo.Current()
			report.Target = updatecheck.Report{Method: "installed", State: "no_external_target_selected", Installed: installed, TargetCommit: installed.GitCommit, TargetVersion: installed.ApplicationVersion, TargetWriterAPI: &writer, TargetMigration: migration, CompatibilityBasis: "compiled_executable"}
		}
		if err != nil {
			report.TargetError = diagnostic.Bounded(err.Error())
		}
		entries, registryErr := project.RegistryAll(cmd.Context(), global)
		if registryErr != nil {
			report.RegistryError = diagnostic.Bounded(registryErr.Error())
		}
		report.Stores = storeUpdates(cmd.Context(), entries, report.Target)
		pkg, pkgErr := clientinstall.Bundled()
		for _, client := range []string{"codex", "claude-code"} {
			observation := clientinstall.Receipt{Client: client, InstalledState: "unknown", Installations: []clientinstall.Installation{}}
			program := client
			if client == "claude-code" {
				program = "claude"
			}
			executable, e := exec.LookPath(program)
			if e == nil && pkgErr == nil {
				bounded, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
				observation, e = (clientinstall.Service{Client: client, Root: filepath.Join(global, "plugins", "installer", "marketplace"), Package: pkg, Run: clientinstall.NativeRunner(executable)}).Observe(bounded)
				cancel()
			}
			if pkgErr != nil {
				e = pkgErr
			}
			if e != nil {
				observation.Problem = diagnostic.Bounded(e.Error())
			}
			report.Plugins = append(report.Plugins, observation)
		}
		if format == "json" {
			return writeJSON(cmd.OutOrStdout(), report)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Target: %s (%s); compatibility: %s\n", report.Target.TargetCommit, report.Target.State, report.Target.CompatibilityBasis)
		if report.TargetError != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "Target unknown:", report.TargetError)
		}
		if report.RegistryError != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "Registry inventory incomplete:", report.RegistryError)
		}
		for _, store := range report.Stores {
			fmt.Fprintf(cmd.OutOrStdout(), "Store: %s\nSchema: %s -> %s\nWriter impact: %s\n", store.Database.Path, store.Database.AppliedMigration, store.TargetMigration, store.WriterImpact)
			if store.Problem != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Attention:", store.Problem)
			}
			if store.MigrationCommand != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "After qualifying/installing the target:", store.MigrationCommand)
			}
		}
		for _, plugin := range report.Plugins {
			fmt.Fprintf(cmd.OutOrStdout(), "Client: %s; owner: %s; installation: %s; bundled version: %s\n", plugin.Client, plugin.MarketplaceOwner, plugin.InstalledState, plugin.PluginVersion)
			if plugin.Problem != "" {
				fmt.Fprintln(cmd.OutOrStdout(), plugin.Problem)
			}
			for _, installation := range plugin.Installations {
				fmt.Fprintf(cmd.OutOrStdout(), "  Scope: %s; project: %s; version: %s; cache: %s; SHA256: %s; expected SHA256: %s\n  Native refresh argv: %q\n", installation.Scope, installation.ProjectPath, installation.Version, installation.Cache.Status, installation.Cache.SHA256, installation.Cache.ExpectedSHA256, installation.Refresh)
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), report.PluginComparisonBasis)
		fmt.Fprintln(cmd.OutOrStdout(), report.RestartGuidance)
		return nil
	}}
	cmd.Flags().StringVar(&source, "source-root", "", "Llewm source checkout; inspect exact HEAD without fetching")
	cmd.Flags().StringVar(&manifest, "manifest", "", "distribution manifest; missing compatibility fields remain unknown")
	cmd.Flags().StringVar(&artifact, "artifact", "", "downloaded artifact to verify against manifest")
	cmd.Flags().StringVar(&target, "target", runtime.GOOS+"/"+runtime.GOARCH, "manifest platform")
	cmd.Flags().StringVar(&output, "output", "text", "text or json")
	return cmd
}
