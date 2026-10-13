package workspacechange

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

const SchemaVersion = 1

type MemberPlan struct {
	Path            string `json:"path"`
	Root            string `json:"root"`
	Store           string `json:"store"`
	ProjectID       string `json:"project_id,omitempty"`
	LineageID       string `json:"lineage_id,omitempty"`
	RecordSetSHA256 string `json:"record_set_sha256,omitempty"`
	Action          string `json:"action"`
}

type Plan struct {
	SchemaVersion  int          `json:"schema_version"`
	Operation      string       `json:"operation"`
	WorkspaceRoot  string       `json:"workspace_root"`
	ManifestSHA256 string       `json:"manifest_sha256"`
	FromMode       string       `json:"from_mode"`
	ToMode         string       `json:"to_mode"`
	LineageID      string       `json:"lineage_id,omitempty"`
	SourceRecords  string       `json:"source_record_set_sha256,omitempty"`
	Members        []MemberPlan `json:"members"`
	TargetStore    string       `json:"target_store,omitempty"`
	TargetProject  string       `json:"target_project_id,omitempty"`
	TargetRecords  string       `json:"target_record_set_sha256,omitempty"`
	Blockers       []string     `json:"blockers"`
	ApplyAllowed   bool         `json:"apply_allowed"`
	Confirmation   string       `json:"confirmation"`
}

type Result struct {
	Plan            Plan   `json:"plan"`
	ImportsApplied  int    `json:"imports_applied"`
	RecordsAppended int    `json:"records_appended"`
	ManifestPath    string `json:"manifest_path"`
	Mode            string `json:"mode"`
}

type storeState struct {
	root      string
	store     string
	projectID string
	identity  *portable.Identity
	db        *sql.DB
}

// BuildPlan inspects every participating store without changing it.
func BuildPlan(ctx context.Context, root, toMode string) (Plan, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Plan{}, err
	}
	workspace, manifestDigest, err := project.LoadWorkspaceWithDigest(root)
	if err != nil {
		return Plan{}, err
	}
	if workspace == nil {
		return Plan{}, fmt.Errorf("%s has no workspace manifest", root)
	}
	if toMode != project.ModeUnified && toMode != project.ModeFederated {
		return Plan{}, fmt.Errorf("mode must be %q or %q", project.ModeUnified, project.ModeFederated)
	}
	if workspace.Mode == toMode {
		return Plan{}, fmt.Errorf("workspace is already %s", toMode)
	}
	plan := Plan{SchemaVersion: SchemaVersion, WorkspaceRoot: root, ManifestSHA256: manifestDigest, FromMode: workspace.Mode, ToMode: toMode, Members: []MemberPlan{}, Blockers: []string{}}
	if toMode == project.ModeFederated {
		plan.Operation = "split"
		var sourceBundle *portable.Bundle
		stores := map[string]string{}
		source, stateErr := inspectStore(ctx, root, false)
		if stateErr != nil {
			plan.Blockers = append(plan.Blockers, "unified source store: "+stateErr.Error())
		} else {
			defer source.db.Close()
			stores[filepath.Clean(source.store)] = "unified source"
			if source.identity == nil {
				plan.Blockers = append(plan.Blockers, "unified source store has no portable lineage; initialize memory sync or export first")
			} else {
				plan.LineageID = source.identity.LineageID
				if bundle, bundleErr := buildBundle(ctx, source); bundleErr != nil {
					plan.Blockers = append(plan.Blockers, "unified source export: "+bundleErr.Error())
				} else {
					plan.SourceRecords = bundle.Manifest.RecordSetSHA256
					sourceBundle = &bundle
				}
			}
		}
		for _, member := range workspace.Members {
			memberRoot := workspace.MemberRoot(member)
			state, stateErr := inspectStore(ctx, memberRoot, true)
			store, storeErr := configuredStore(memberRoot)
			item := MemberPlan{Path: member.Path, Root: memberRoot, Store: store, Action: "create_and_import"}
			if storeErr != nil {
				plan.Blockers = append(plan.Blockers, member.Path+": "+storeErr.Error())
			}
			if stateErr == nil && state != nil {
				item.Store, item.ProjectID = state.store, state.projectID
				if state.identity != nil {
					item.LineageID = state.identity.LineageID
					if bundle, bundleErr := buildBundle(ctx, state); bundleErr != nil {
						plan.Blockers = append(plan.Blockers, member.Path+" existing store export: "+bundleErr.Error())
					} else {
						item.RecordSetSHA256 = bundle.Manifest.RecordSetSHA256
					}
				}
				item.Action = "import"
			} else if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
				plan.Blockers = append(plan.Blockers, member.Path+": "+stateErr.Error())
			}
			if item.LineageID != "" && plan.LineageID != "" && item.LineageID != plan.LineageID {
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s has lineage %s, expected %s", member.Path, item.LineageID, plan.LineageID))
			} else if stateErr == nil && state != nil && state.identity != nil && sourceBundle != nil {
				if blocker := importBlocker(ctx, state, *sourceBundle); blocker != "" {
					plan.Blockers = append(plan.Blockers, member.Path+": "+blocker)
				}
			}
			if state != nil {
				state.db.Close()
			}
			cleanStore := filepath.Clean(item.Store)
			if previous, exists := stores[cleanStore]; item.Store != "" && exists {
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s and %s resolve to the same store %s", previous, member.Path, item.Store))
			} else if item.Store != "" {
				stores[cleanStore] = member.Path
			}
			plan.Members = append(plan.Members, item)
		}
	} else {
		plan.Operation = "merge"
		lineages := map[string]bool{}
		bundles := map[string]portable.Bundle{}
		records := map[string]string{}
		stores := map[string]string{}
		for _, member := range workspace.Members {
			memberRoot := workspace.MemberRoot(member)
			state, stateErr := inspectStore(ctx, memberRoot, false)
			store, storeErr := configuredStore(memberRoot)
			item := MemberPlan{Path: member.Path, Root: memberRoot, Store: store, Action: "export_and_import"}
			if storeErr != nil {
				plan.Blockers = append(plan.Blockers, member.Path+": "+storeErr.Error())
			}
			if stateErr != nil {
				plan.Blockers = append(plan.Blockers, member.Path+": "+stateErr.Error())
			} else {
				item.Store, item.ProjectID = state.store, state.projectID
				if state.identity == nil {
					plan.Blockers = append(plan.Blockers, member.Path+": portable lineage is not initialized")
				} else {
					item.LineageID = state.identity.LineageID
					lineages[item.LineageID] = true
					if bundle, bundleErr := buildBundle(ctx, state); bundleErr != nil {
						plan.Blockers = append(plan.Blockers, member.Path+" export: "+bundleErr.Error())
					} else {
						item.RecordSetSHA256 = bundle.Manifest.RecordSetSHA256
						bundles[member.Path] = bundle
						for _, record := range bundle.Records {
							key := fmt.Sprintf("%s/%s/%d", record.Envelope.RecordType, record.Envelope.RecordID, record.Envelope.RecordRevision)
							if existing, ok := records[key]; ok && existing != record.Envelope.EnvelopeSHA256 {
								plan.Blockers = append(plan.Blockers, fmt.Sprintf("members contain a portable collision at %s", key))
							} else {
								records[key] = record.Envelope.EnvelopeSHA256
							}
						}
					}
				}
				state.db.Close()
			}
			cleanStore := filepath.Clean(item.Store)
			if previous, exists := stores[cleanStore]; item.Store != "" && exists {
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s and %s resolve to the same store %s", previous, member.Path, item.Store))
			} else if item.Store != "" {
				stores[cleanStore] = member.Path
			}
			plan.Members = append(plan.Members, item)
		}
		if len(lineages) == 1 {
			for lineage := range lineages {
				plan.LineageID = lineage
			}
		} else if len(lineages) > 1 {
			values := make([]string, 0, len(lineages))
			for lineage := range lineages {
				values = append(values, lineage)
			}
			sort.Strings(values)
			plan.Blockers = append(plan.Blockers, "federated members have different lineages: "+strings.Join(values, ", "))
		}
		target, stateErr := inspectStore(ctx, root, true)
		plan.TargetStore, err = configuredStore(root)
		if err != nil {
			plan.Blockers = append(plan.Blockers, "unified target store: "+err.Error())
		}
		if stateErr == nil && target != nil {
			plan.TargetStore, plan.TargetProject = target.store, target.projectID
			if target.identity != nil && plan.LineageID != "" && target.identity.LineageID != plan.LineageID {
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("unified target has lineage %s, expected %s", target.identity.LineageID, plan.LineageID))
			} else if target.identity != nil {
				if bundle, bundleErr := buildBundle(ctx, target); bundleErr != nil {
					plan.Blockers = append(plan.Blockers, "unified target export: "+bundleErr.Error())
				} else {
					plan.TargetRecords = bundle.Manifest.RecordSetSHA256
				}
				for member, bundle := range bundles {
					if blocker := importBlocker(ctx, target, bundle); blocker != "" {
						plan.Blockers = append(plan.Blockers, member+" -> unified target: "+blocker)
					}
				}
			}
			target.db.Close()
		} else if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
			plan.Blockers = append(plan.Blockers, "unified target store: "+stateErr.Error())
		}
		if previous, exists := stores[filepath.Clean(plan.TargetStore)]; plan.TargetStore != "" && exists {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s and unified target resolve to the same store %s", previous, plan.TargetStore))
		}
	}
	plan.ApplyAllowed = len(plan.Blockers) == 0 && plan.LineageID != ""
	plan.Confirmation, err = confirmation(plan)
	return plan, err
}

// Apply rechecks the reviewed plan, transfers memory through portable bundles,
// and changes the manifest only after every import succeeds.
func Apply(ctx context.Context, root, toMode, confirm string) (Result, error) {
	plan, err := BuildPlan(ctx, root, toMode)
	if err != nil {
		return Result{}, err
	}
	if confirm == "" || confirm != plan.Confirmation {
		return Result{Plan: plan}, fmt.Errorf("explicit review required: rerun with --apply --confirm %s", plan.Confirmation)
	}
	if !plan.ApplyAllowed {
		return Result{Plan: plan}, fmt.Errorf("workspace %s is blocked: %s", plan.Operation, strings.Join(plan.Blockers, "; "))
	}
	result := Result{Plan: plan, ManifestPath: project.WorkspacePath(plan.WorkspaceRoot), Mode: toMode}
	if plan.Operation == "split" {
		source, err := openStore(ctx, plan.WorkspaceRoot)
		if err != nil {
			return result, err
		}
		bundle, err := buildBundle(ctx, source)
		source.db.Close()
		if err != nil {
			return result, err
		}
		if bundle.Manifest.RecordSetSHA256 != plan.SourceRecords {
			return result, fmt.Errorf("unified source memory changed after review; preview the split again")
		}
		for _, member := range plan.Members {
			target, err := ensureStore(ctx, member.Root, plan.LineageID)
			if err != nil {
				return result, fmt.Errorf("prepare %s: %w", member.Path, err)
			}
			applied, applyErr := portable.ApplyOperationalImport(ctx, target.db, target.projectID, bundle.ManifestBytes, bundle.RecordsBytes, time.Time{})
			target.db.Close()
			if applyErr != nil {
				return result, fmt.Errorf("import %s: %w", member.Path, applyErr)
			}
			result.ImportsApplied++
			result.RecordsAppended += applied.Plan.Counts[portable.PlanAppend]
		}
	} else {
		target, err := ensureStore(ctx, plan.WorkspaceRoot, plan.LineageID)
		if err != nil {
			return result, fmt.Errorf("prepare unified target: %w", err)
		}
		defer target.db.Close()
		for _, member := range plan.Members {
			source, err := openStore(ctx, member.Root)
			if err != nil {
				return result, err
			}
			bundle, buildErr := buildBundle(ctx, source)
			source.db.Close()
			if buildErr != nil {
				return result, fmt.Errorf("export %s: %w", member.Path, buildErr)
			}
			if bundle.Manifest.RecordSetSHA256 != member.RecordSetSHA256 {
				return result, fmt.Errorf("%s memory changed after review; preview the merge again", member.Path)
			}
			applied, applyErr := portable.ApplyOperationalImport(ctx, target.db, target.projectID, bundle.ManifestBytes, bundle.RecordsBytes, time.Time{})
			if applyErr != nil {
				return result, fmt.Errorf("import %s: %w", member.Path, applyErr)
			}
			result.ImportsApplied++
			result.RecordsAppended += applied.Plan.Counts[portable.PlanAppend]
		}
		if err := target.db.Close(); err != nil {
			return result, err
		}
		target.db = nil
	}
	if _, err := project.ChangeWorkspaceMode(plan.WorkspaceRoot, plan.FromMode, plan.ToMode, plan.ManifestSHA256); err != nil {
		return result, err
	}
	return result, nil
}

func inspectStore(ctx context.Context, root string, missingOK bool) (*storeState, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(cfg.Storage.Database); err != nil {
		if missingOK && errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	db, err := database.OpenReadOnly(ctx, cfg.Storage.Database)
	if err != nil {
		return nil, err
	}
	resolution, err := (project.Resolver{DB: db}).Lookup(ctx, root, project.StoreInside(cfg.Storage.Database, root))
	if err != nil {
		db.Close()
		return nil, err
	}
	identity, err := (portable.Service{DB: db}).Get(ctx, resolution.ProjectID)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &storeState{root: root, store: cfg.Storage.Database, projectID: resolution.ProjectID, identity: identity, db: db}, nil
}

func openStore(ctx context.Context, root string) (*storeState, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	db, err := database.Open(ctx, cfg.Storage.Database)
	if err != nil {
		return nil, err
	}
	resolution, err := (project.Resolver{DB: db}).Lookup(ctx, root, project.StoreInside(cfg.Storage.Database, root))
	if err != nil {
		db.Close()
		return nil, err
	}
	identity, err := (portable.Service{DB: db}).Get(ctx, resolution.ProjectID)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &storeState{root: root, store: cfg.Storage.Database, projectID: resolution.ProjectID, identity: identity, db: db}, nil
}

func ensureStore(ctx context.Context, root, lineage string) (*storeState, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	if _, err := config.WriteDefault(root); err != nil {
		return nil, err
	}
	db, err := database.Initialize(ctx, cfg.Storage.Database)
	if err != nil {
		return nil, err
	}
	if err := database.RaiseWriterFloor(ctx, db, buildinfo.WriterAPIVersion); err != nil {
		db.Close()
		return nil, err
	}
	resolution, err := (project.Resolver{DB: db}).Ensure(ctx, root, cfg.Project.Name, project.StoreInside(cfg.Storage.Database, root))
	if err != nil {
		db.Close()
		return nil, err
	}
	identity, err := (portable.Service{DB: db}).Initialize(ctx, resolution.ProjectID, lineage)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &storeState{root: root, store: cfg.Storage.Database, projectID: resolution.ProjectID, identity: &identity, db: db}, nil
}

func configuredStore(root string) (string, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return "", err
	}
	return cfg.Storage.Database, nil
}

func buildBundle(ctx context.Context, source *storeState) (portable.Bundle, error) {
	loaded, err := portable.LoadOperationalRecords(ctx, source.db, source.projectID)
	if err != nil {
		return portable.Bundle{}, err
	}
	return portable.BuildBundle(portable.BundleSpec{
		ExportID: "export:" + activity.NewID(), LineageID: loaded.Identity.LineageID,
		SourceInstanceID: loaded.Identity.InstanceID, CreatedAt: time.Now().UTC(),
		SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1",
		RedactionProfile: portable.DefaultRedactionProfile(), Omissions: portable.CuratedV1Omissions(),
	}, loaded.Records)
}

func importBlocker(ctx context.Context, target *storeState, bundle portable.Bundle) string {
	plan, err := portable.PlanOperationalImport(ctx, target.db, target.projectID, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil {
		return "portable import check failed: " + err.Error()
	}
	if plan.ApplyAllowed {
		return ""
	}
	return fmt.Sprintf("portable import is not applicable (collision=%d unsupported=%d invalid=%d)", plan.Counts[portable.PlanCollision], plan.Counts[portable.PlanUnsupported], plan.Counts[portable.PlanInvalid])
}

func confirmation(plan Plan) (string, error) {
	plan.Confirmation = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
