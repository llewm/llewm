package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/foldersync"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
	"github.com/spf13/cobra"
)

func projectArchive(application *app.App) (recordsync.Archive, error) {
	identity, err := application.Portable.Get(context.Background(), application.ProjectID)
	if err != nil {
		return recordsync.Archive{}, err
	}
	if identity == nil {
		return recordsync.Archive{}, fmt.Errorf("portable identity required; explicitly create or join a lineage with sync init")
	}
	return recordsync.OpenArchive(filepath.Join(config.StateDir(application.Root), "history"), identity.LineageID)
}

func rebuildHistoryInventory(ctx context.Context, application *app.App, archive recordsync.Archive) error {
	settings := application.Config.Sync
	folderInventories := func(ctx context.Context) (map[string][]recordsync.Block, error) {
		inventories := make(map[string][]recordsync.Block, len(settings.Folders))
		for _, endpoint := range settings.Folders {
			clone, _ := settings.MemoryClone()
			if err := foldersync.Separate(endpoint.Directory, config.StateDir(application.Root), application.Config.Storage.Database, application.Config.Backups.Dir, filepath.Join(application.Root, ".git"), clone); err != nil {
				return nil, err
			}
			inventory, err := (foldersync.Syncer{DB: application.DB, ProjectID: application.ProjectID, Archive: archive, Endpoint: endpoint}).Inventory(ctx)
			if err != nil {
				return nil, err
			}
			inventories[endpoint.ID()] = inventory
		}
		return inventories, nil
	}
	if settings.Remote != "" {
		syncer, err := projectSyncer(application)
		if err != nil {
			return err
		}
		syncer.Archive = &archive
		return syncer.RebuildInventory(ctx, folderInventories)
	}
	inventories, err := folderInventories(ctx)
	if err != nil {
		return err
	}
	return (recordsync.Index{DB: application.DB, ProjectID: application.ProjectID, Lineage: archive.Lineage}).Rebuild(ctx, inventories)
}

type historyResult struct {
	Git     *memsync.Result     `json:"git,omitempty"`
	Folders []foldersync.Result `json:"folders"`
}

func runHistorySync(ctx context.Context, application *app.App, session, prompt bool) (historyResult, error) {
	settings := application.Config.Sync
	if !settings.Configured() {
		return historyResult{}, fmt.Errorf("no History sync endpoint configured; use sync folder add or sync init --remote")
	}
	archive, err := projectArchive(application)
	if err != nil {
		return historyResult{}, err
	}
	// Folder sync needs a local snapshot before its first pass. The Git cycle
	// takes its own snapshot under the clone lock after observing tracked
	// provenance, so a Git-only sync must not export and capture it twice.
	if len(settings.Folders) > 0 {
		if _, err := archive.CaptureProject(ctx, application.DB, application.ProjectID); err != nil {
			return historyResult{}, err
		}
	}
	var result historyResult
	results := map[string]foldersync.Result{}
	var failures []error
	pass := func() {
		for _, endpoint := range settings.Folders {
			clone, _ := settings.MemoryClone()
			if err := foldersync.Separate(endpoint.Directory, config.StateDir(application.Root), application.Config.Storage.Database, application.Config.Backups.Dir, filepath.Join(application.Root, ".git"), clone); err != nil {
				results[endpoint.Name] = foldersync.Result{Endpoint: endpoint.Name, Outcome: "unavailable", Detail: err.Error(), PeerReceipt: "unknown"}
				failures = append(failures, err)
				continue
			}
			value, err := (foldersync.Syncer{DB: application.DB, ProjectID: application.ProjectID, Archive: archive, Endpoint: endpoint}).Sync(ctx)
			if previous, ok := results[endpoint.Name]; ok {
				value.PublishedRecords += previous.PublishedRecords
				value.ObservedRecords += previous.ObservedRecords
				value.ImportedRecords += previous.ImportedRecords
			}
			results[endpoint.Name] = value
			if err != nil {
				failures = append(failures, fmt.Errorf("folder %s: %w", endpoint.Name, err))
			}
		}
	}
	pass()
	// Rebuild only after every configured endpoint supplies a complete,
	// independently validated snapshot. Git keeps its clone lock throughout.
	if len(failures) > 0 {
		indexOnly := true
		for _, failure := range failures {
			if !errors.Is(failure, recordsync.ErrIndexRebuild) {
				indexOnly = false
				break
			}
		}
		if indexOnly {
			rebuildErr := rebuildHistoryInventory(ctx, application, archive)
			if rebuildErr != nil {
				failures = append(failures, fmt.Errorf("history inventory rebuild: %w", rebuildErr))
			} else {
				failures = nil
				pass()
			}
		}
	}
	if settings.Remote != "" {
		syncer, err := projectSyncer(application)
		if err != nil {
			failures = append(failures, err)
		} else {
			syncer.Archive = &archive
			syncer.Relay = settings.RelayToGit
			syncer.NoPrompt = !prompt
			var value memsync.Result
			if session {
				value, err = syncer.Session(ctx, settings.SessionTimeout())
			} else {
				value, err = syncer.Sync(ctx)
			}
			if errors.Is(err, recordsync.ErrIndexRebuild) && len(failures) == 0 {
				if rebuildErr := rebuildHistoryInventory(ctx, application, archive); rebuildErr != nil {
					err = errors.Join(err, fmt.Errorf("history inventory rebuild: %w", rebuildErr))
				} else {
					previous := value
					if session {
						value, err = syncer.Session(ctx, settings.SessionTimeout())
					} else {
						value, err = syncer.Sync(ctx)
					}
					value.ExportedRecords += previous.ExportedRecords
					value.ImportedRecords += previous.ImportedRecords
				}
			}
			result.Git = &value
			if err != nil {
				failures = append(failures, err)
			}
			if value.SemanticPending != "" {
				failures = append(failures, fmt.Errorf("Git transport completed but semantic import is pending"))
			}
			if value.Outcome == memsync.OutcomeCollision {
				failures = append(failures, fmt.Errorf("Git sync has record collisions"))
			}
		}
	}
	// Forward newly arrived objects to earlier endpoints without an extra network
	// round trip. Relaying to each destination remains explicitly authorized.
	if len(settings.Folders) > 1 || settings.Remote != "" {
		pass()
	}
	for _, endpoint := range settings.Folders {
		result.Folders = append(result.Folders, results[endpoint.Name])
	}
	return result, errors.Join(failures...)
}

type historyStatus struct {
	memsync.Status
	Folders []foldersync.Result `json:"folders,omitempty"`
}

func (s historyStatus) Line(now time.Time) string {
	if len(s.Folders) == 0 {
		return s.Status.Line(now)
	}
	var lines []string
	if s.Remote != "" {
		lines = append(lines, s.Status.Line(now))
	}
	for _, endpoint := range s.Folders {
		line := fmt.Sprintf("Folder %s: last local outcome %s at %s; peer receipt %s", endpoint.Endpoint, endpoint.Outcome, endpoint.At.UTC().Format(time.RFC3339), endpoint.PeerReceipt)
		if endpoint.Unchanged {
			line += " · unchanged locally"
		} else if endpoint.DurationMS > 0 {
			line += " · full local cycle"
		}
		if endpoint.DurationMS > 0 {
			line += " · duration " + (time.Duration(endpoint.DurationMS) * time.Millisecond).String()
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, " · ")
}

func syncFolderCommand() *cobra.Command {
	parent := &cobra.Command{Use: "folder", Short: "Configure a shared-folder History sync endpoint; provider delivery remains external"}
	var directory, lineage, storeID string
	var create, relay bool
	add := &cobra.Command{Use: "add NAME", Short: "Explicitly pin a folder identity and lineage; optionally create its marker", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] == "git" || !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`).MatchString(args[0]) {
			return fmt.Errorf("folder name must be lowercase letters/digits, _ or -, starting with a letter (at most32)")
		}
		if directory == "" || lineage == "" {
			return fmt.Errorf("--directory and --confirm-lineage are required")
		}
		return withApp(cmd, func(application *app.App) error {
			identity, err := application.Portable.Get(cmd.Context(), application.ProjectID)
			if err != nil {
				return err
			}
			if identity == nil || identity.LineageID != lineage {
				return fmt.Errorf("explicitly join/create the intended lineage with sync init first")
			}
			settings := application.Config.Sync
			if len(settings.Folders) >= 8 {
				return fmt.Errorf("at most eight folder endpoints are supported")
			}
			for _, e := range settings.Folders {
				if e.Name == args[0] {
					return fmt.Errorf("folder endpoint name already configured")
				}
			}
			absolute, err := filepath.Abs(directory)
			if err != nil {
				return err
			}
			clone, err := settings.MemoryClone()
			if err != nil {
				return err
			}
			if err := foldersync.Separate(absolute, config.StateDir(application.Root), application.Config.Storage.Database, application.Config.Backups.Dir, filepath.Join(application.Root, ".git"), clone); err != nil {
				return err
			}
			marker, err := foldersync.Pin(absolute, storeID, create)
			if err != nil {
				return err
			}
			for _, e := range settings.Folders {
				if e.StoreID == marker.StoreID {
					return fmt.Errorf("folder store already configured")
				}
			}
			endpoint := foldersync.Endpoint{Name: args[0], Directory: absolute, StoreID: marker.StoreID, LineageID: lineage, Relay: relay}
			settings.Folders = append(settings.Folders, endpoint)
			if _, err := config.SetSync(application.Root, settings); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), endpoint)
		})
	}}
	add.Flags().StringVar(&directory, "directory", "", "existing real directory managed by your chosen sync application")
	add.Flags().StringVar(&lineage, "confirm-lineage", "", "exact selected lineage (required)")
	add.Flags().StringVar(&storeID, "store-id", "", "exact inspected marker identity; required when joining an existing folder")
	add.Flags().BoolVar(&create, "create", false, "explicitly create a new immutable marker in the existing folder")
	add.Flags().BoolVar(&relay, "relay", false, "authorize publishing this lineage's retained records learned through other endpoints into this folder")
	inspect := &cobra.Command{Use: "inspect DIRECTORY", PersistentPreRunE: func(*cobra.Command, []string) error { return nil }, Short: "Read a folder marker without opening a project database", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		value, err := foldersync.Inspect(args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), value)
	}}
	remove := &cobra.Command{Use: "remove NAME", Short: "Remove local endpoint configuration; preserve shared files and local history", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return withApp(cmd, func(application *app.App) error {
			settings := application.Config.Sync
			found := false
			for i, e := range settings.Folders {
				if e.Name == args[0] {
					settings.Folders = append(settings.Folders[:i], settings.Folders[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("folder endpoint not found")
			}
			_, err := config.SetSync(application.Root, settings)
			return err
		})
	}}
	var stopped bool
	convert := &cobra.Command{Use: "convert NAME", Short: "Convert a folder lineage to history blocks", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !stopped {
			return fmt.Errorf("folder conversion requires --writers-stopped after stopping all old writers")
		}
		return withApp(cmd, func(application *app.App) error {
			for _, endpoint := range application.Config.Sync.Folders {
				if endpoint.Name != args[0] {
					continue
				}
				clone, err := application.Config.Sync.MemoryClone()
				if err != nil {
					return err
				}
				if err := foldersync.Separate(endpoint.Directory, config.StateDir(application.Root), application.Config.Storage.Database, application.Config.Backups.Dir, filepath.Join(application.Root, ".git"), clone); err != nil {
					return err
				}
				archive, err := projectArchive(application)
				if err != nil {
					return err
				}
				if err := (foldersync.Syncer{DB: application.DB, ProjectID: application.ProjectID, Archive: archive, Endpoint: endpoint}).Convert(cmd.Context()); err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Folder conversion prepared; external delivery and peer receipt remain unconfirmed.")
				return err
			}
			return fmt.Errorf("folder endpoint not found")
		})
	}}
	convert.Flags().BoolVar(&stopped, "writers-stopped", false, "confirm all older sync writers are stopped")
	parent.AddCommand(add, inspect, remove, convert)
	return parent
}

func syncRelayCommand() *cobra.Command {
	var lineage string
	var enabled bool
	command := &cobra.Command{Use: "relay ENDPOINT", Short: "Authorize or disable relaying retained history to one destination (git or folder name)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("enable") || lineage == "" {
			return fmt.Errorf("--enable=true/false and --confirm-lineage are required")
		}
		return withApp(cmd, func(application *app.App) error {
			identity, err := application.Portable.Get(cmd.Context(), application.ProjectID)
			if err != nil {
				return err
			}
			if identity == nil || identity.LineageID != lineage {
				return fmt.Errorf("confirmed lineage differs from selected project")
			}
			settings := application.Config.Sync
			destination := ""
			if args[0] == "git" && settings.Remote != "" {
				settings.RelayToGit = enabled
				destination = redact.String(settings.Remote)
			} else {
				for i, e := range settings.Folders {
					if e.Name == args[0] {
						settings.Folders[i].Relay = enabled
						destination = e.Directory
					}
				}
			}
			if destination == "" {
				return fmt.Errorf("configured endpoint not found")
			}
			if _, err := config.SetSync(application.Root, settings); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{"endpoint": args[0], "destination": destination, "lineage": lineage, "relay": enabled})
		})
	}}
	command.Flags().StringVar(&lineage, "confirm-lineage", "", "exact selected lineage (required)")
	command.Flags().BoolVar(&enabled, "enable", false, "explicit true or false: permit this destination to receive retained history from other endpoints")
	return command
}

func readFolderStatuses(application *app.App) []foldersync.Result {
	var result []foldersync.Result
	identity, err := application.Portable.Get(context.Background(), application.ProjectID)
	if err != nil || identity == nil {
		return result
	}
	archive := recordsync.Archive{Directory: filepath.Join(config.StateDir(application.Root), "history"), Lineage: identity.LineageID}
	for _, e := range application.Config.Sync.Folders {
		value := foldersync.Result{Endpoint: e.Name, Outcome: "never_run", PeerReceipt: "unknown"}
		if err := archive.Status(e.ID(), &value); err != nil && !errors.Is(err, os.ErrNotExist) {
			value.Outcome = "status_unavailable"
		}
		value.Endpoint = e.Name
		result = append(result, value)
	}
	return result
}
