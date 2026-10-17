package app

import (
	"github.com/llewm/llewm/components/continuity/internal/adoption"

	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/brief"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/audit"
	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/clientperm"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/contracts"
	"github.com/llewm/llewm/components/continuity/internal/coverage"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/fork"
	"github.com/llewm/llewm/components/continuity/internal/friction"
	"github.com/llewm/llewm/components/continuity/internal/history"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/pin"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/presence"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/projectpulse"
	"github.com/llewm/llewm/components/continuity/internal/readiness"
	"github.com/llewm/llewm/components/continuity/internal/release"
	"github.com/llewm/llewm/components/continuity/internal/resource"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/standinggrant"
	"github.com/llewm/llewm/components/continuity/internal/timeline"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/usage"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
	"github.com/llewm/llewm/components/continuity/internal/workstate"
)

type App struct {
	Root      string
	Config    config.Config
	DB        *sql.DB
	ProjectID string
	Identity  project.Resolution
	// Workspace is the unified workspace manifest at Root, or nil.
	Workspace *project.Workspace
	// Repositories selects the Git checkout for commit-anchored operations.
	Repositories project.Repositories
	// AfterCheckpoint, when set, runs after a checkpoint is newly saved.
	AfterCheckpoint func()
	Events          activity.Store
	Checkpoints     checkpoint.Service
	// CheckpointRecovery carries a refused structured checkpoint across a
	// required writer restart without treating transcript context as state.
	CheckpointRecovery checkpoint.RecoveryStore
	Procedures         procedures.Service
	Resources          resource.Service
	Pins               pin.Service
	Presence           presence.Service
	Leases             lease.Service
	// LeasePolicy is the configured per-scope fencing policy.
	LeasePolicy    lease.Policy
	StandingGrants standinggrant.Service
	Contracts      contracts.Service
	Bootstrap      bootstrap.Compiler
	Audit          audit.Service
	Attribution    attribution.Service
	Roadmap        roadmap.Service
	Releases       release.Service
	Decisions      decision.Service
	History        history.Service
	Topics         topic.Service
	Narratives     worknarrative.Service
	Friction       friction.Service
	WorkItems      workitem.Service
	Coverage       coverage.Service
	TrackPlans     trackplan.Service
	Portable       portable.Service
	Pulse          projectpulse.Service
	Readiness      readiness.Service
	Work           workstate.Service
	Timeline       timeline.Service
}

func Open(ctx context.Context, root string) (*App, error) {
	return open(ctx, project.Checkout{Root: root}, false)
}

// OpenCheckout opens the project for a resolved checkout, so commit-anchored
// operations in a workspace default to the member containing the working directory.
func OpenCheckout(ctx context.Context, checkout project.Checkout) (*App, error) {
	return open(ctx, checkout, false)
}

// OpenCheckoutReadOnly is OpenCheckout under the OpenReadOnly contract.
func OpenCheckoutReadOnly(ctx context.Context, checkout project.Checkout) (*App, error) {
	return open(ctx, checkout, true)
}

// OpenReadOnly serves projections without registering or relocating the project or allowing writes.
func OpenReadOnly(ctx context.Context, root string) (*App, error) {
	return open(ctx, project.Checkout{Root: root}, true)
}

func open(ctx context.Context, checkout project.Checkout, readOnly bool) (*App, error) {
	root := checkout.Root
	if !readOnly {
		if err := adoption.CheckActive(config.StateDir(root)); err != nil {
			return nil, err
		}
	}
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	phase, err := adoption.Load(config.StateDir(root))
	if err != nil {
		return nil, err
	}
	if phase.Phase() == "trial" || phase.Phase() == "exited" {
		cfg.Sync = config.Sync{}
	}
	globalRoot, err := config.GlobalDir()
	if err != nil {
		return nil, err
	}
	var db *sql.DB
	if readOnly {
		db, err = database.OpenReadOnly(ctx, cfg.Storage.Database)
	} else {
		db, err = database.Open(ctx, cfg.Storage.Database)
	}
	if err != nil {
		return nil, err
	}
	events := activity.Store{DB: db}
	resolver := project.Resolver{DB: db}
	storeInsideRoot := project.StoreInside(cfg.Storage.Database, root)
	var identity project.Resolution
	if readOnly {
		identity, err = resolver.Lookup(ctx, root, storeInsideRoot)
	} else {
		identity, err = resolver.Ensure(ctx, root, cfg.Project.Name, storeInsideRoot)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	projectID := identity.ProjectID
	workspaceRoot := root
	if checkout.Workspace != "" {
		workspaceRoot = checkout.Workspace
	}
	workspace, err := project.LoadWorkspace(workspaceRoot)
	if err != nil {
		db.Close()
		return nil, err
	}
	if workspace != nil && workspace.Mode == project.ModeUnified && !readOnly {
		if err := resolver.SyncMembers(ctx, projectID, *workspace); err != nil {
			db.Close()
			return nil, err
		}
	}
	if !readOnly {
		// Only a write-incompatible build raises the floor. MCP tool changes alone
		// must not strand an active session before it can checkpoint.
		if err := database.RaiseWriterFloor(ctx, db, buildinfo.WriterAPIVersion); err != nil {
			db.Close()
			return nil, err
		}
	}
	if !readOnly && identity.RepositoryID != "" {
		repositoryRoot := checkout.Path
		if repositoryRoot == "" {
			repositoryRoot = root
		}
		fingerprint, fingerprintErr := project.GitRootCommit(ctx, repositoryRoot)
		if fingerprintErr != nil {
			db.Close()
			return nil, fingerprintErr
		}
		if fingerprint != "" {
			lineage := ""
			if portableIdentity, identityErr := (portable.Service{DB: db}).Get(ctx, projectID); identityErr != nil {
				db.Close()
				return nil, identityErr
			} else if portableIdentity != nil {
				lineage = portableIdentity.LineageID
			}
			entry := project.RegistryEntry{Fingerprint: fingerprint, RepositoryID: identity.RepositoryID, ProjectID: projectID, LineageID: lineage, Root: repositoryRoot, Store: cfg.Storage.Database}
			if workspace != nil && workspace.Mode == project.ModeFederated {
				entry.GroupRoot, entry.GroupName, entry.Member = workspace.Root, workspace.Name, checkout.Member
			}
			if err := project.ObserveRegistry(ctx, globalRoot, entry); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	repositories := project.Repositories{Root: root, Default: checkout.Member}
	if workspace != nil && workspace.Mode == project.ModeUnified {
		repositories.Workspace = workspace
	}
	var roadmapRepository roadmap.Repository
	if repositories.Workspaced() {
		roadmapRepository = roadmap.WorkspaceRepository{Repositories: repositories}
	}
	roadmapService := roadmap.Service{DB: db, Root: root, ProjectName: cfg.Project.Name, Repo: roadmapRepository}
	narratives := worknarrative.Service{DB: db, Root: root, Repositories: repositories}
	checkpoints := checkpoint.Service{DB: db}
	coreRules, coreRulesDisabled, coreRulesWarning := resolveCoreRules(globalRoot, cfg)
	expectedMigration, err := database.CompiledLatestMigration()
	if err != nil {
		db.Close()
		return nil, err
	}
	planPolicy, err := config.LoadPlanPolicy(globalRoot, cfg.PlanPolicy)
	if err != nil {
		db.Close()
		return nil, err
	}
	work := workstate.Service{Root: root, Pulse: projectpulse.Service{Checkpoints: checkpoints, Roadmap: roadmapService}, Roadmap: roadmapService, Plans: trackplan.Service{DB: db, Root: root, Policy: planPolicy}}
	leasePolicy, err := lease.ParsePolicy(cfg.Leases.Required)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("config [leases] required: %w", err)
	}
	if err := (usage.Prices{Overrides: cfg.Usage.Prices}).Validate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("config [usage.prices]: %w", err)
	}
	if cfg.Leases.Remote && cfg.Sync.Remote == "" {
		db.Close()
		return nil, errors.New("config [leases] remote shares leases through the memory repository; set it up first with `llewm sync init --remote URL`")
	}
	var leaseSkew time.Duration
	if cfg.Leases.Skew != "" {
		if leaseSkew, err = time.ParseDuration(cfg.Leases.Skew); err != nil || leaseSkew <= 0 {
			db.Close()
			return nil, fmt.Errorf("config [leases] skew %q must be a positive duration such as \"45s\"", cfg.Leases.Skew)
		}
	}
	return &App{
		Root: root, Config: cfg, DB: db, ProjectID: projectID, Identity: identity, Workspace: workspace, Repositories: repositories, Events: events,
		Checkpoints:        checkpoints,
		CheckpointRecovery: checkpoint.RecoveryStore{StateDir: config.StateDir(root), ProjectID: projectID},
		Procedures:         procedures.Service{DB: db},
		Resources:          resource.Service{DB: db},
		Pins:               pin.Service{DB: db},
		Presence:           presence.Service{DB: db},
		Leases:             lease.Service{DB: db, Policy: leasePolicy, Skew: leaseSkew},
		LeasePolicy:        leasePolicy,
		StandingGrants:     standinggrant.Service{DB: db},
		Contracts:          contracts.Service{DB: db, Root: root},
		Bootstrap: bootstrap.Compiler{Checkpoints: checkpoints, MaxBytes: cfg.Bootstrap.MaxBytes, GlobalRoot: globalRoot, CoreRules: coreRules, CoreRulesDisabled: coreRulesDisabled, CoreRulesWarning: coreRulesWarning, Pins: func(ctx context.Context, projectID, taskID string) ([]pin.Line, error) {
			return pin.ForTask(ctx, db, projectID, taskID)
		}, CodeCheck: func(ctx context.Context, projectID, checkpointID string) string {
			lags, err := codeBehind(ctx, db, repositories, projectID, checkpointID)
			if err != nil {
				return ""
			}
			return CodeBehindLine(checkpointID, lags)
		}, Coordination: func(ctx context.Context, projectID string, latest *checkpoint.Checkpoint) (string, string) {
			return changedUnderneath(ctx, db, repositories, projectID, latest), overlapNotice(ctx, db, repositories, projectID, latest, time.Now().UTC())
		}, Reviews: func(ctx context.Context, projectID string, latest *checkpoint.Checkpoint) (string, string) {
			return reviewNotice(ctx, db, projectID, latest)
		}, ForkCheck: func(ctx context.Context, projectID, taskID string) string {
			forks, err := fork.CheckpointForks(ctx, db, projectID, "")
			if err != nil {
				return ""
			}
			notice := ForkNotice(forks, taskID)
			if revisions, err := fork.RevisionForks(ctx, db, projectID, ""); err == nil {
				notice = strings.TrimSpace(notice + " " + RevisionForkNotice(revisions))
			}
			if supersessions, err := fork.SupersessionForks(ctx, db, projectID, ""); err == nil {
				notice = strings.TrimSpace(notice + " " + SupersessionForkNotice(supersessions))
			}
			if writes, err := lease.StaleWrites(ctx, db, projectID, ""); err == nil {
				notice = strings.TrimSpace(notice + " " + lease.StaleNotice(writes))
			}
			return notice
		}, Briefs: func(ctx context.Context, projectID, nextAction string) []string {
			pending, err := checkpoints.PendingTexts(ctx, projectID)
			if err != nil {
				return nil
			}
			items, err := brief.MentionedOrPending(ctx, db, projectID, nextAction, pending)
			if err != nil {
				return nil
			}
			var lines []string
			for _, item := range items {
				lines = append(lines, brief.Line(item))
			}
			return lines
		}, LeaseCheck: func(ctx context.Context, projectID, taskID string) string {
			var nodes []string
			if taskID != "" {
				var err error
				if nodes, err = pin.TaskRoadmapNodes(ctx, db, projectID, taskID); err != nil {
					return ""
				}
			}
			notice, err := lease.Notice(ctx, db, projectID, taskID, nodes, time.Now().UTC())
			if err != nil {
				return ""
			}
			return notice
		}, GrantCheck: func(ctx context.Context, projectID string) string {
			notice, err := standinggrant.Notice(ctx, db, projectID, time.Now().UTC())
			if err != nil {
				return ""
			}
			return notice
		}, ContractCheck: func(ctx context.Context, projectID string) string {
			report, err := contracts.Service{DB: db, Root: root}.Check(ctx, projectID)
			if err != nil {
				return ""
			}
			return contracts.Notice(report)
		}, RecoveryCheck: func(_ context.Context, projectID string) string {
			store := checkpoint.RecoveryStore{StateDir: config.StateDir(root), ProjectID: projectID}
			values, err := store.List()
			if err != nil || len(values) == 0 {
				return ""
			}
			ids := make([]string, 0, min(len(values), 3))
			for _, value := range values[:min(len(values), 3)] {
				ids = append(ids, value.ID)
			}
			notice := fmt.Sprintf("%d structured checkpoint envelope(s) await `llewm checkpoint recovery apply ID` or explicit discard: %s", len(values), strings.Join(ids, ", "))
			if len(values) > len(ids) {
				notice += fmt.Sprintf(" (+%d more)", len(values)-len(ids))
			}
			return notice
		}, WorkItemCheck: func(ctx context.Context, projectID string) string {
			counts, err := workitem.Service{DB: db}.Counts(ctx, projectID)
			if err != nil {
				return ""
			}
			return workitem.Notice(counts)
		}, Choose: func(ctx context.Context, projectID, sessionID string, selection checkpoint.Selection) *bootstrap.Choice {
			return chooseTask(ctx, db, repositories, projectID, sessionID, selection, time.Now().UTC())
		}},
		Audit:       audit.Service{DB: db},
		Attribution: attribution.Service{DB: db},
		Roadmap:     roadmapService,
		Releases:    release.Service{DB: db, Root: root},
		Decisions:   decision.Service{DB: db},
		History:     history.Service{DB: db},
		Topics:      topic.Service{DB: db},
		Narratives:  narratives,
		Friction:    friction.Service{DB: db},
		WorkItems:   workitem.Service{DB: db},
		Coverage:    coverage.Service{DB: db, Root: root},
		TrackPlans:  trackplan.Service{DB: db, Root: root, Policy: planPolicy},
		Portable:    portable.Service{DB: db},
		Pulse:       projectpulse.Service{Checkpoints: checkpoints, Roadmap: roadmapService},
		Readiness: readiness.Service{Root: root, DatabasePath: cfg.Storage.Database, DB: db, ProjectID: projectID, Narratives: narratives, InstalledPath: buildinfo.Current().Executable, ExpectedMCPAPI: buildinfo.MCPAPIVersion, ExpectedMigration: expectedMigration, ClientWarnings: func() []string {
			homes, err := clientperm.DefaultHomes()
			if err != nil {
				return nil
			}
			return clientperm.Warnings(clientperm.Detect(homes, root, nil))
		}},
		Work:     work,
		Timeline: timeline.Service{DB: db, Work: work},
	}, nil
}

func (a *App) Close() error { return a.DB.Close() }
