package portfolio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/foldersync"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

type DestinationResult struct {
	Kind           string              `json:"kind"`
	Directory      string              `json:"directory,omitempty"`
	StoreID        string              `json:"store_id,omitempty"`
	FolderAttempts []foldersync.Result `json:"folder_attempts,omitempty"`
	FolderStatus   *foldersync.Result  `json:"folder_status,omitempty"`
	Name           string              `json:"name"`
	Remote         string              `json:"remote,omitempty"`
	Clone          string              `json:"clone,omitempty"`
	Relay          bool                `json:"relay"`
	SSHKey         string              `json:"ssh_key,omitempty"`
	Outcome        string              `json:"outcome"`
	Attempts       []memsync.Result    `json:"attempts,omitempty"`
	Error          string              `json:"error,omitempty"`
	Status         *memsync.Status     `json:"status,omitempty"`
}

type Result struct {
	Project
	ExistingProjectSync bool                `json:"existing_project_sync"`
	Outcome             string              `json:"outcome"`
	Destinations        []DestinationResult `json:"destinations"`
	Error               string              `json:"error,omitempty"`
}

// Inspect resolves the actual owning root and reads an existing identity. It
// neither initializes a project nor creates a lineage, archive or memory clone.
func Inspect(ctx context.Context, path string) (Project, error) {
	if os.Getenv("CONTINUITY_HOME") != "" {
		return Project{}, fmt.Errorf("portfolio routing requires project-local configuration; unset CONTINUITY_HOME")
	}
	checkout, err := project.Resolve(path)
	if err != nil {
		return Project{}, err
	}
	root, err := filepath.EvalSymlinks(checkout.Root)
	if err != nil {
		return Project{}, err
	}
	a, err := app.OpenReadOnly(ctx, root)
	if err != nil {
		return Project{}, err
	}
	defer a.Close()
	id, err := a.Portable.Get(ctx, a.ProjectID)
	if err != nil {
		return Project{}, err
	}
	if id == nil {
		return Project{}, fmt.Errorf("existing portable lineage required; use sync init explicitly before binding a project")
	}
	return Project{Root: root, ProjectID: a.ProjectID, Lineage: id.LineageID}, nil
}

func verify(ctx context.Context, p Project) (config.Config, error) {
	actual, err := Inspect(ctx, p.Root)
	if err != nil {
		return config.Config{}, err
	}
	if actual.Root != p.Root || actual.ProjectID != p.ProjectID || actual.Lineage != p.Lineage {
		return config.Config{}, fmt.Errorf("project root, ID or lineage changed; review and explicitly replace the portfolio binding")
	}
	phase, err := adoption.Load(config.StateDir(p.Root))
	if err != nil {
		return config.Config{}, err
	}
	if phase.Phase() == "trial" || phase.Phase() == "exited" {
		return config.Config{}, fmt.Errorf("History sync is unavailable during trial or after exit")
	}
	return config.Load(p.Root)
}

// ConvertRoute explicitly converts one pinned project/destination route.
// The caller must stop all older writers before invoking this operation.
func ConvertRoute(ctx context.Context, c Catalog, projectName, destinationName string) error {
	var selected *Project
	for i := range c.Projects {
		if c.Projects[i].Name == projectName {
			selected = &c.Projects[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("portfolio project not found")
	}
	var route *Route
	for i := range selected.Routes {
		if selected.Routes[i].Destination == destinationName {
			route = &selected.Routes[i]
			break
		}
	}
	destination, ok := c.Destination(destinationName)
	if route == nil || !ok {
		return fmt.Errorf("portfolio route not found")
	}
	cfg, err := verify(ctx, *selected)
	if err != nil {
		return err
	}
	if err := ValidateStorage(c, destination, cfg, *selected); err != nil {
		return err
	}
	a, err := app.Open(ctx, selected.Root)
	if err != nil {
		return err
	}
	defer a.Close()
	identity, err := a.Portable.Get(ctx, a.ProjectID)
	if err != nil || identity == nil || identity.LineageID != selected.Lineage || a.ProjectID != selected.ProjectID {
		return fmt.Errorf("project binding changed before conversion")
	}
	archive, err := recordsync.OpenArchive(filepath.Join(config.StateDir(selected.Root), "history"), selected.Lineage)
	if err != nil {
		return err
	}
	if destination.Kind == "folder" {
		endpoint := foldersync.Endpoint{Name: destination.Name, Directory: destination.Directory, StoreID: destination.StoreID, LineageID: selected.Lineage, Relay: route.Relay}
		return (foldersync.Syncer{DB: a.DB, ProjectID: a.ProjectID, Archive: archive, Endpoint: endpoint}).Convert(ctx)
	}
	if destination.Kind != "git" {
		return fmt.Errorf("unsupported portfolio destination")
	}
	clone, err := destination.MemoryClone()
	if err != nil {
		return err
	}
	access := memsync.Access{NoPrompt: true, SSHKey: destination.SSHKey}
	setupCtx, cancel := context.WithTimeout(ctx, memsync.NetworkTimeout)
	err = memsync.Init(setupCtx, destination.Remote, clone, access, memsync.Setup{})
	cancel()
	if err != nil {
		return err
	}
	syncer := memsync.Syncer{DB: a.DB, ProjectID: a.ProjectID, Remote: destination.Remote, Clone: clone, SSHKey: destination.SSHKey, NoPrompt: true, Archive: &archive, Relay: route.Relay}
	result, err := syncer.Convert(ctx)
	if err != nil {
		return err
	}
	if result.Outcome != memsync.OutcomeSynced || result.UnpushedCommits != 0 {
		return fmt.Errorf("portfolio conversion did not sync: %s", result.Detail)
	}
	return nil
}

// Run previews or synchronizes selected routes. Selection is explicit even from
// an empty folder. Each destination has its own transport receipt;
// records reach it only through the selected project's lineage export/archive.
func Run(ctx context.Context, c Catalog, name string, all, dryRun bool) ([]Result, error) {
	return runSelected(ctx, c, name, all, dryRun, true)
}

func runSelected(ctx context.Context, c Catalog, name string, all, dryRun, inspectFolders bool) ([]Result, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if (name == "" && !all) || (name != "" && all) {
		return nil, fmt.Errorf("select one project NAME or --all")
	}
	var results []Result
	var failures []error
	for _, p := range c.Projects {
		if !all && p.Name != name {
			continue
		}
		value, err := runProject(ctx, c, p, dryRun, inspectFolders)
		results = append(results, value)
		if err != nil {
			failures = append(failures, fmt.Errorf("project %s: %w", p.Name, err))
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no selected portfolio project")
	}
	return results, errors.Join(failures...)
}

// ReadStatus reads endpoint-scoped attempt history without contacting a remote,
// creating a clone/archive, registering a project or opening a writable store.
func ReadStatus(ctx context.Context, c Catalog, name string, all bool) ([]Result, error) {
	results, previewErr := runSelected(ctx, c, name, all, true, false)
	failures := []error{previewErr}
	for i := range results {
		r := &results[i]
		if r.Error != "" {
			continue
		}
		a, err := app.OpenReadOnly(ctx, r.Root)
		if err != nil {
			r.Outcome, r.Error = "error", redact.String(err.Error())
			failures = append(failures, err)
			continue
		}
		r.Outcome = "status"
		for j := range r.Destinations {
			d := &r.Destinations[j]
			if d.Kind == "folder" {
				archive := recordsync.Archive{Directory: filepath.Join(config.StateDir(r.Root), "history"), Lineage: r.Lineage}
				value := foldersync.Result{Endpoint: d.Name, Outcome: "never_run", PeerReceipt: "unknown"}
				err := archive.Status("folder:"+d.StoreID, &value)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					d.Outcome, d.Error = "error", redact.String(err.Error())
					failures = append(failures, err)
					continue
				}
				value.Endpoint = d.Name
				d.FolderStatus, d.Outcome = &value, value.Outcome
				continue
			}
			status, err := memsync.ReadRemoteStatus(ctx, a.DB, a.ProjectID, d.Remote)
			if err != nil {
				d.Outcome, d.Error = "error", redact.String(err.Error())
				failures = append(failures, err)
				continue
			}
			status.Configured, status.Remote, status.Clone = true, d.Remote, d.Clone
			d.Status, d.Outcome = &status, "never_run"
			if status.Latest != nil {
				d.Outcome = status.Latest.Outcome
			}
		}
		a.Close()
		updated, statusErr := finish(*r, "status")
		*r = updated
		failures = append(failures, statusErr)
	}
	return results, errors.Join(failures...)
}

func runProject(ctx context.Context, c Catalog, p Project, dryRun, inspectFolders bool) (result Result, err error) {
	result = Result{Project: p, Outcome: "error"}
	defer func() {
		if err != nil {
			result.Error = redact.String(err.Error())
		}
	}()
	cfg, err := verify(ctx, p)
	if err != nil {
		return result, err
	}
	result.ExistingProjectSync = cfg.Sync.Configured()
	for _, route := range p.Routes {
		destination, _ := c.Destination(route.Destination)
		d := DestinationResult{Name: destination.Name, Kind: destination.Kind, Remote: destination.Remote, Directory: destination.Directory, StoreID: destination.StoreID, SSHKey: destination.SSHKey, Relay: route.Relay, Outcome: "preview"}
		if destination.Kind == "git" {
			d.Clone, err = destination.MemoryClone()
		}
		if err == nil {
			err = ValidateStorage(c, destination, cfg, p)
		}
		if err == nil && destination.Kind == "folder" && dryRun && inspectFolders {
			var marker foldersync.Marker
			marker, err = foldersync.Inspect(d.Directory)
			if err == nil && marker.StoreID != d.StoreID {
				err = fmt.Errorf("folder store differs from pinned destination")
			}
		}
		if err != nil {
			d.Outcome, d.Error = "unavailable", redact.String(err.Error())
			err = nil
		}
		result.Destinations = append(result.Destinations, d)
	}
	if dryRun {
		return finish(result, "preview")
	}
	// Recheck the binding after opening writable storage and before any remote
	// operation. No identity is created during preview or configuration.
	a, err := app.Open(ctx, p.Root)
	if err != nil {
		return result, err
	}
	defer a.Close()
	identity, err := a.Portable.Get(ctx, a.ProjectID)
	if err != nil {
		return result, err
	}
	if identity == nil || identity.LineageID != p.Lineage || a.ProjectID != p.ProjectID {
		return result, fmt.Errorf("project binding changed before sync")
	}
	archive, err := recordsync.OpenArchive(filepath.Join(config.StateDir(p.Root), "history"), p.Lineage)
	if err != nil {
		return result, err
	}
	// Two bounded passes let a late endpoint's imports reach earlier endpoints.
	// Retained observations are forwarded only with that route's relay consent.
	passes := 1
	if len(result.Destinations) > 1 {
		passes = 2
	}
	for pass := 0; pass < passes; pass++ {
		for i := range result.Destinations {
			d := &result.Destinations[i]
			if d.Error != "" {
				continue // preserve an independent failure, without retry loops
			}
			if err := ctx.Err(); err != nil {
				d.Outcome, d.Error = "error", err.Error()
				continue
			}
			if d.Kind == "folder" {
				local, exportErr := portable.EncodeMemoryExport(ctx, a.DB, a.ProjectID)
				if exportErr == nil {
					exportErr = archive.CaptureLocal(local.Envelopes)
				}
				if exportErr != nil {
					d.Outcome, d.Error = "error", redact.String(exportErr.Error())
					continue
				}
				attempt, syncErr := (foldersync.Syncer{DB: a.DB, ProjectID: a.ProjectID, Archive: archive, Endpoint: foldersync.Endpoint{Name: d.Name, Directory: d.Directory, StoreID: d.StoreID, LineageID: p.Lineage, Relay: d.Relay}}).Sync(ctx)
				d.FolderAttempts = append(d.FolderAttempts, attempt)
				d.Outcome = attempt.Outcome
				if syncErr != nil {
					d.Error = redact.String(syncErr.Error())
				} else if attempt.Outcome != "published_locally" {
					d.Error = "folder has pending synchronization or semantic import; inspect its receipt"
				}
				continue
			}
			access := memsync.Access{NoPrompt: true, SSHKey: d.SSHKey}
			syncer := memsync.Syncer{DB: a.DB, ProjectID: a.ProjectID, Remote: d.Remote, Clone: d.Clone, SSHKey: d.SSHKey, NoPrompt: true, Archive: &archive, Relay: d.Relay}
			if pass == 0 {
				setupCtx, cancel := context.WithTimeout(ctx, memsync.NetworkTimeout)
				setupErr := memsync.Init(setupCtx, d.Remote, d.Clone, access, memsync.Setup{})
				cancel()
				if setupErr != nil {
					d.Outcome, d.Error = "error", redact.String(setupErr.Error())
					failure, recordErr := syncer.RecordFailure(ctx, d.Error)
					d.Attempts = append(d.Attempts, failure)
					if recordErr != nil {
						d.Error += "; recording failure: " + redact.String(recordErr.Error())
					}
					continue
				}
			}
			attempt, syncErr := syncer.Sync(ctx)
			d.Attempts = append(d.Attempts, attempt)
			d.Outcome = attempt.Outcome
			if syncErr != nil {
				d.Error = redact.String(syncErr.Error())
			} else if attempt.Outcome != memsync.OutcomeSynced || attempt.SemanticPending != "" || attempt.Collisions != 0 || attempt.DeferredRecords != 0 {
				d.Error = "destination has pending synchronization or semantic import; inspect its receipt"
			}
		}
	}
	outcome := "synced"
	for _, d := range result.Destinations {
		if d.Kind == "folder" {
			outcome = "published_locally"
		}
	}
	return finish(result, outcome)
}

func finish(result Result, outcome string) (Result, error) {
	var failures []error
	for _, d := range result.Destinations {
		if d.Error != "" {
			failures = append(failures, fmt.Errorf("destination %s: %s", d.Name, d.Error))
		}
	}
	result.Outcome = outcome
	if len(failures) > 0 {
		result.Outcome = "partial"
	}
	return result, errors.Join(failures...)
}

// ValidateStorage rejects overlap across the catalog and selected project's
// operational storage, including endpoints used outside portfolio routing.
// It does not create paths or contact destinations.
func ValidateStorage(c Catalog, d Destination, cfg config.Config, p Project) error {
	path := d.Directory
	if d.Kind == "git" {
		var err error
		path, err = d.MemoryClone()
		if err != nil {
			return err
		}
	}
	protected := []string{config.StateDir(p.Root), cfg.Storage.Database, cfg.Backups.Dir}
	if global, err := config.GlobalDir(); err != nil {
		return err
	} else {
		protected = append(protected, global)
	}
	for _, member := range c.Projects {
		protected = append(protected, member.Root)
	}
	// The same pinned endpoint can be configured at both project and portfolio
	// scope. Different endpoints must never share storage.
	for _, folder := range cfg.Sync.Folders {
		if d.Kind != "folder" || folder.StoreID != d.StoreID || folder.Directory != d.Directory {
			protected = append(protected, folder.Directory)
		}
	}
	if cfg.Sync.Remote != "" && (d.Kind != "git" || cfg.Sync.Remote != d.Remote) {
		clone, err := cfg.Sync.MemoryClone()
		if err != nil {
			return err
		}
		protected = append(protected, clone)
	}
	for _, other := range c.Destinations {
		if other.Name == d.Name {
			continue
		}
		otherPath := other.Directory
		if other.Kind == "git" {
			var err error
			otherPath, err = other.MemoryClone()
			if err != nil {
				return err
			}
		}
		protected = append(protected, otherPath)
	}
	return separate(path, protected...)
}

// resolvePath resolves existing ancestor symlinks without creating a prospective
// clone. A dangling symlink is an error, not an absent directory to create later.
func resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current, suffix := abs, ""
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(resolved, suffix), nil
		}
		if _, statErr := os.Lstat(current); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("cannot resolve storage path")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = filepath.Join(filepath.Base(current), suffix)
		current = parent
	}
}

func separate(path string, protected ...string) error {
	resolved, err := resolvePath(path)
	if err != nil {
		return err
	}
	for _, other := range protected {
		if other == "" {
			continue
		}
		p, err := resolvePath(other)
		if err != nil {
			return err
		}
		for _, pair := range [][2]string{{resolved, p}, {p, resolved}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err == nil && (rel == "." || filepath.IsLocal(rel)) {
				return fmt.Errorf("overlapping storage trees")
			}
		}
	}
	return nil
}
