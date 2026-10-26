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
	"github.com/llewm/llewm/components/continuity/internal/memsync"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

type DestinationResult struct {
	Name     string           `json:"name"`
	Remote   string           `json:"remote"`
	Clone    string           `json:"clone"`
	Relay    bool             `json:"relay"`
	SSHKey   string           `json:"ssh_key,omitempty"`
	Outcome  string           `json:"outcome"`
	Attempts []memsync.Result `json:"attempts,omitempty"`
	Error    string           `json:"error,omitempty"`
	Status   *memsync.Status  `json:"status,omitempty"`
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

// Run previews or synchronizes selected routes. Selection is explicit even from
// an empty folder. Each destination has its own clone and import watermark;
// records reach it only through the selected project's lineage export/archive.
func Run(ctx context.Context, c Catalog, name string, all, dryRun bool) ([]Result, error) {
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
		value, err := runProject(ctx, c, p, dryRun)
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
	results, previewErr := Run(ctx, c, name, all, true)
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
	}
	return results, errors.Join(failures...)
}

func runProject(ctx context.Context, c Catalog, p Project, dryRun bool) (result Result, err error) {
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
		clone, err := destination.MemoryClone()
		if err != nil {
			return result, err
		}
		// A portfolio clone must never be inside any participating code checkout,
		// store, backup or shared folder (or contain one).
		protected := []string{config.StateDir(p.Root), cfg.Storage.Database, cfg.Backups.Dir}
		for _, member := range c.Projects {
			protected = append(protected, member.Root)
		}
		for _, folder := range cfg.Sync.Folders {
			protected = append(protected, folder.Directory)
		}
		for _, other := range c.Destinations {
			if other.Name == destination.Name {
				continue
			}
			otherClone, err := other.MemoryClone()
			if err != nil {
				return result, err
			}
			protected = append(protected, otherClone)
		}
		if err := separate(clone, protected...); err != nil {
			return result, fmt.Errorf("portfolio clone overlaps protected project storage: %w", err)
		}
		result.Destinations = append(result.Destinations, DestinationResult{Name: destination.Name, Remote: destination.Remote, Clone: clone, SSHKey: destination.SSHKey, Relay: route.Relay, Outcome: "preview"})
	}
	if dryRun {
		result.Outcome = "preview"
		return result, nil
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
	var failures []error
	for _, d := range result.Destinations {
		if d.Error != "" {
			failures = append(failures, fmt.Errorf("destination %s: %s", d.Name, d.Error))
		}
	}
	if len(failures) > 0 {
		result.Outcome = "partial"
	} else {
		result.Outcome = "synced"
	}
	return result, errors.Join(failures...)
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
