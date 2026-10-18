package readiness

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/gitprotection"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/redact"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

const (
	SchemaVersion    = 1
	MaxProbes        = 16
	MaxVerifications = 16
)

type Verification struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Authority string `json:"authority"`
	Detail    string `json:"detail,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
}

type Source struct {
	Root              string         `json:"root"`
	GitCommit         string         `json:"git_commit,omitempty"`
	Dirty             *bool          `json:"dirty,omitempty"`
	ExpectedMCPAPI    int            `json:"expected_mcp_api"`
	ExpectedMigration string         `json:"expected_migration"`
	Verification      []Verification `json:"verification"`
	Authority         string         `json:"authority"`
	Error             string         `json:"error,omitempty"`
}

type Installed struct {
	Executable buildinfo.ExecutableInfo `json:"executable"`
	MCPAPI     *int                     `json:"mcp_api,omitempty"`
	Authority  string                   `json:"authority"`
}

type Database struct {
	Path               string `json:"path"`
	AppliedMigration   string `json:"applied_migration,omitempty"`
	ExpectedMigration  string `json:"expected_migration"`
	State              string `json:"state"`
	Integrity          string `json:"integrity"`
	IntegrityAuthority string `json:"integrity_authority"`
	Authority          string `json:"authority"`
	Error              string `json:"error,omitempty"`
}

type Runtime struct {
	Observed         bool                    `json:"observed"`
	ProcessStartedAt *time.Time              `json:"process_started_at,omitempty"`
	State            *buildinfo.RuntimeState `json:"state,omitempty"`
	Health           *buildinfo.Health       `json:"health,omitempty"`
	Authority        string                  `json:"authority"`
	Reason           string                  `json:"reason,omitempty"`
}

type Report struct {
	GitProtection         gitprotection.Report `json:"git_protection"`
	ProjectState          adoption.Status      `json:"project_state"`
	SchemaVersion         int                  `json:"schema_version"`
	Stage                 string               `json:"stage"`
	Source                Source               `json:"source"`
	Installed             Installed            `json:"installed"`
	Database              Database             `json:"database"`
	Runtime               Runtime              `json:"runtime"`
	Probes                []Verification       `json:"probes"`
	UnmetGates            []string             `json:"unmet_gates"`
	RecommendedNextAction string               `json:"recommended_next_action"`
	// Warnings are advisory observations that are not gates, such as a
	// client missing the permission rules Continuity needs.
	Warnings   []string `json:"warnings"`
	Disclaimer string   `json:"disclaimer"`
}

type Input struct {
	Runtime             *buildinfo.RuntimeState
	ProcessStartedAt    *time.Time
	Probes              []string
	SourceVerifications []string
	CheckIntegrity      bool
}

type Service struct {
	Root              string
	DatabasePath      string
	DB                *sql.DB
	ProjectID         string
	Repo              roadmap.Repository
	Narratives        worknarrative.Service
	InstalledPath     string
	ExpectedMCPAPI    int
	ExpectedMigration string
	// ClientWarnings returns client-setup warnings. Nil means no check.
	ClientWarnings func() []string
}

func (s Service) Report(ctx context.Context, input Input) (Report, error) {
	if len(input.Probes) > MaxProbes {
		return Report{}, fmt.Errorf("probes may contain at most %d items", MaxProbes)
	}
	if len(input.SourceVerifications) > MaxVerifications {
		return Report{}, fmt.Errorf("source verifications may contain at most %d items", MaxVerifications)
	}
	for index, value := range input.Probes {
		value = strings.TrimSpace(redact.String(value))
		if value == "" || len(value) > 256 || strings.IndexByte(value, 0) >= 0 {
			return Report{}, fmt.Errorf("probe %d must be a non-empty redacted value of at most 256 bytes", index)
		}
		input.Probes[index] = value
	}
	for index, value := range input.SourceVerifications {
		value = strings.TrimSpace(redact.String(value))
		if value == "" || len(value) > 512 || strings.IndexByte(value, 0) >= 0 {
			return Report{}, fmt.Errorf("source verification %d must be a non-empty redacted value of at most 512 bytes", index)
		}
		input.SourceVerifications[index] = value
	}
	cfg, err := config.Load(s.Root)
	if err != nil {
		return Report{}, err
	}
	state, err := adoption.Inspect(config.StateDir(s.Root), cfg.Mode)
	if err != nil {
		return Report{}, err
	}
	result := Report{
		ProjectState:  state,
		SchemaVersion: SchemaVersion,
		Source:        Source{Root: s.Root, ExpectedMCPAPI: s.ExpectedMCPAPI, ExpectedMigration: s.ExpectedMigration, Verification: []Verification{}, Authority: "machine_observed"},
		Installed:     Installed{Authority: "machine_observed"},
		Database:      Database{Path: s.DatabasePath, ExpectedMigration: s.ExpectedMigration, State: "unknown", Integrity: "not_checked", IntegrityAuthority: "unavailable", Authority: "machine_observed"},
		Runtime:       Runtime{Authority: "machine_observed"}, Probes: []Verification{}, UnmetGates: []string{}, Warnings: []string{},
		Disclaimer: "Readiness is a deterministic view of current observations and attributable supplied evidence; it is not a mutable completion flag or execution authorization.",
	}
	for _, item := range input.SourceVerifications {
		if item = strings.TrimSpace(item); item != "" {
			result.Source.Verification = append(result.Source.Verification, Verification{Name: item, Status: "reported_pass", Authority: "caller_reported"})
		}
	}
	repo := s.Repo
	if repo == nil {
		repo = roadmap.GitRepository{Root: s.Root}
	}
	head, headErr := repo.Head(ctx)
	dirty, dirtyErr := repo.Dirty(ctx)
	if headErr != nil || dirtyErr != nil {
		result.Source.Error = joinErrors(headErr, dirtyErr)
	} else {
		result.Source.GitCommit = head
		result.Source.Dirty = &dirty
	}
	result.Installed.Executable = buildinfo.InspectExecutable(s.InstalledPath)
	if input.Runtime != nil && input.Runtime.ExecutableSHA256 != "" && input.Runtime.ExecutableSHA256 == result.Installed.Executable.SHA256 {
		api := input.Runtime.Build.MCPAPIVersion
		result.Installed.MCPAPI = &api
	} else if result.Source.Dirty != nil && !*result.Source.Dirty && result.Source.GitCommit != "" && result.Installed.Executable.GitCommit == result.Source.GitCommit && !result.Installed.Executable.BuildDirty {
		api := result.Source.ExpectedMCPAPI
		result.Installed.MCPAPI = &api
	}
	applied, err := database.LatestMigration(ctx, s.DB)
	if err != nil {
		result.Database.Error = err.Error()
	} else {
		result.Database.AppliedMigration = applied
		result.Database.State = migrationState(applied, s.ExpectedMigration)
	}
	if input.CheckIntegrity {
		var integrity string
		if err := s.DB.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
			result.Database.Integrity = "error"
			result.Database.IntegrityAuthority = "machine_observed"
			result.Database.Error = joinText(result.Database.Error, err.Error())
		} else {
			result.Database.Integrity = integrity
			result.Database.IntegrityAuthority = "machine_observed"
		}
	}
	if input.Runtime == nil {
		result.Runtime.Reason = "no long-running MCP process identity was supplied"
	} else {
		result.Runtime.Observed = true
		result.Runtime.State = input.Runtime
		health := input.Runtime.CheckInstalled()
		result.Runtime.Health = &health
		result.Runtime.ProcessStartedAt = input.ProcessStartedAt
	}
	for _, probe := range input.Probes {
		result.Probes = append(result.Probes, s.probe(ctx, probe))
	}
	result.GitProtection = gitprotection.Inspect(ctx, s.Root)
	result.Warnings = append(result.Warnings, result.GitProtection.Warnings()...)
	derive(&result)
	if s.ClientWarnings != nil {
		result.Warnings = append(result.Warnings, s.ClientWarnings()...)
	}
	return result, nil
}

func (s Service) probe(ctx context.Context, value string) Verification {
	value = strings.TrimSpace(value)
	result := Verification{Name: value, Status: "failed", Authority: "machine_observed", Evidence: value}
	if !strings.HasPrefix(value, "narrative:") {
		result.Detail = "unsupported probe; supported form is narrative:<id>"
		return result
	}
	shown, err := s.Narratives.Show(ctx, s.ProjectID, value, worknarrative.DefaultRenderBytes)
	if err != nil {
		result.Detail = err.Error()
		return result
	}
	if shown == nil || shown.Record.State != "current" {
		result.Detail = "current narrative did not resolve"
		return result
	}
	result.Status = "passed"
	result.Detail = "current work narrative resolved through the live service"
	return result
}

func migrationState(applied, expected string) string {
	switch {
	case applied == "" || expected == "":
		return "unknown"
	case applied == expected:
		return "current"
	case applied < expected:
		return "pending"
	default:
		return "ahead"
	}
}

func derive(result *Report) {
	sourceKnown := result.Source.Error == "" && result.Source.GitCommit != "" && result.Source.Dirty != nil
	sourceClean := sourceKnown && !*result.Source.Dirty
	installedKnown := result.Installed.Executable.Error == "" && result.Installed.Executable.MetadataError == "" && result.Installed.Executable.SHA256 != ""
	installedAPIKnown := result.Installed.MCPAPI != nil
	installedMatches := sourceClean && installedKnown && installedAPIKnown && *result.Installed.MCPAPI == result.Source.ExpectedMCPAPI && result.Installed.Executable.GitCommit == result.Source.GitCommit && !result.Installed.Executable.BuildDirty
	databaseCurrent := result.Database.State == "current"
	integrityFailed := result.Database.IntegrityAuthority == "machine_observed" && result.Database.Integrity != "ok"
	runtimeKnown := result.Runtime.Observed && result.Runtime.State != nil && result.Runtime.Health != nil
	runtimeCurrent := runtimeKnown && result.Runtime.Health.Status == "current" && result.Runtime.State.Build.GitCommit == result.Source.GitCommit && result.Runtime.State.Build.MCPAPIVersion == result.Source.ExpectedMCPAPI
	sourceVerified := len(result.Source.Verification) > 0
	for _, verification := range result.Source.Verification {
		sourceVerified = sourceVerified && (verification.Status == "passed" || verification.Status == "reported_pass")
	}
	probesPassed := len(result.Probes) > 0
	for _, probe := range result.Probes {
		probesPassed = probesPassed && probe.Status == "passed"
	}

	if !sourceKnown {
		result.UnmetGates = append(result.UnmetGates, "repository identity unavailable")
	}
	if sourceKnown && !sourceClean {
		result.UnmetGates = append(result.UnmetGates, "repository has uncommitted changes")
	}
	if !sourceVerified {
		result.UnmetGates = append(result.UnmetGates, "no attributable source verification was supplied")
	}
	if !installedKnown {
		result.UnmetGates = append(result.UnmetGates, "installed executable metadata unavailable")
	}
	if installedKnown && result.Installed.MCPAPI == nil {
		result.UnmetGates = append(result.UnmetGates, "installed executable API unavailable")
	}
	if sourceClean && installedKnown && !installedMatches {
		result.UnmetGates = append(result.UnmetGates, "installed executable does not match clean repository HEAD")
	}
	if result.Database.State == "unknown" {
		result.UnmetGates = append(result.UnmetGates, "database migration state unavailable")
	}
	if result.Database.State == "pending" {
		result.UnmetGates = append(result.UnmetGates, "database migration is pending")
	}
	if result.Database.State == "ahead" {
		result.UnmetGates = append(result.UnmetGates, "database schema is ahead of this binary")
	}
	if integrityFailed {
		result.UnmetGates = append(result.UnmetGates, "database integrity check did not return ok")
	}
	if !runtimeKnown {
		result.UnmetGates = append(result.UnmetGates, "running MCP identity unavailable")
	}
	if runtimeKnown && !runtimeCurrent {
		result.UnmetGates = append(result.UnmetGates, "running MCP identity is stale or does not match repository HEAD")
	}
	if len(result.Probes) == 0 {
		result.UnmetGates = append(result.UnmetGates, "no required live surface probes were supplied")
	}
	if len(result.Probes) > 0 && !probesPassed {
		result.UnmetGates = append(result.UnmetGates, "one or more required live surface probes failed")
	}

	switch {
	case result.Database.State == "ahead" || integrityFailed || runtimeKnown && result.Runtime.Health.Status == "current" && result.Runtime.State.Build.GitCommit != result.Installed.Executable.GitCommit:
		result.Stage = "inconsistent"
		result.RecommendedNextAction = "diagnose the conflicting database or executable identities before making changes"
	case !sourceKnown || !installedKnown || !installedAPIKnown || result.Database.State == "unknown" || !runtimeKnown:
		result.Stage = "unknown"
		result.RecommendedNextAction = "obtain the unavailable repository, executable, database, or running MCP observation"
	case !installedMatches:
		result.Stage = "source_only"
		result.RecommendedNextAction = "build and install an artifact from the clean repository HEAD"
	case !databaseCurrent:
		result.Stage = "installed_migration_pending"
		result.RecommendedNextAction = "back up and migrate the configured project database with the installed artifact"
	case !runtimeCurrent:
		result.Stage = "migrated_restart_pending"
		result.RecommendedNextAction = "restart the client so it launches the installed executable, then rerun readiness"
	case !sourceVerified:
		result.Stage = "identity_current"
		result.RecommendedNextAction = "supply attributable source-verification references for the intended commit"
	case !probesPassed:
		result.Stage = "identity_current"
		result.RecommendedNextAction = "run the named required live surface probes"
	default:
		result.Stage = "operationally_verified"
		result.RecommendedNextAction = "none; the supplied readiness gates are operationally verified"
	}
}

func joinErrors(values ...error) string {
	parts := []string{}
	for _, value := range values {
		if value != nil {
			parts = append(parts, value.Error())
		}
	}
	return strings.Join(parts, "; ")
}

func joinText(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return fmt.Sprintf("%s; %s", left, right)
}
