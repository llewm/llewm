package readiness

import (
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
)

func boolPointer(value bool) *bool { return &value }

func baseReport() Report {
	api := 28
	state := &buildinfo.RuntimeState{Build: buildinfo.Info{GitCommit: "source", MCPAPIVersion: api}, ExecutableSHA256: "hash"}
	health := &buildinfo.Health{Status: "current", Runtime: *state, Installed: buildinfo.ExecutableInfo{GitCommit: "source", SHA256: "hash"}}
	return Report{
		Source:     Source{GitCommit: "source", Dirty: boolPointer(false), ExpectedMCPAPI: api, ExpectedMigration: "019.sql", Verification: []Verification{{Name: "go test ./...", Status: "reported_pass", Authority: "caller_reported"}}},
		Installed:  Installed{Executable: buildinfo.ExecutableInfo{GitCommit: "source", SHA256: "hash"}, MCPAPI: &api},
		Database:   Database{AppliedMigration: "019.sql", ExpectedMigration: "019.sql", State: "current"},
		Runtime:    Runtime{Observed: true, State: state, Health: health},
		Probes:     []Verification{{Name: "narrative:probe", Status: "passed", Authority: "machine_observed"}},
		UnmetGates: []string{},
	}
}

func TestDeriveReadinessStages(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(*Report)
		stage  string
		action string
	}{
		{name: "operationally verified", stage: "operationally_verified", action: "none; the supplied readiness gates are operationally verified"},
		{name: "source ahead of install", edit: func(r *Report) {
			r.Installed.Executable.GitCommit = "older"
			r.Runtime.State.Build.GitCommit = "older"
			r.Runtime.Health.Runtime.Build.GitCommit = "older"
			r.Runtime.Health.Installed.GitCommit = "older"
		}, stage: "source_only", action: "build and install an artifact from the clean repository HEAD"},
		{name: "installed migration pending", edit: func(r *Report) { r.Database.AppliedMigration = "018.sql"; r.Database.State = "pending" }, stage: "installed_migration_pending", action: "back up and migrate the configured project database with the installed artifact"},
		{name: "migrated stale runtime", edit: func(r *Report) {
			r.Runtime.State.Build.GitCommit = "older"
			r.Runtime.Health.Status = "stale"
			r.Runtime.Health.RestartRequired = true
		}, stage: "migrated_restart_pending", action: "restart the client so it launches the installed executable, then rerun readiness"},
		{name: "identity current without probes", edit: func(r *Report) { r.Probes = []Verification{} }, stage: "identity_current", action: "run the named required live surface probes"},
		{name: "dirty source", edit: func(r *Report) { r.Source.Dirty = boolPointer(true) }, stage: "source_only", action: "build and install an artifact from the clean repository HEAD"},
		{name: "database ahead", edit: func(r *Report) { r.Database.AppliedMigration = "020.sql"; r.Database.State = "ahead" }, stage: "inconsistent", action: "diagnose the conflicting database or executable identities before making changes"},
		{name: "integrity failure", edit: func(r *Report) { r.Database.Integrity = "corrupt"; r.Database.IntegrityAuthority = "machine_observed" }, stage: "inconsistent", action: "diagnose the conflicting database or executable identities before making changes"},
		{name: "missing runtime", edit: func(r *Report) { r.Runtime = Runtime{} }, stage: "unknown", action: "obtain the unavailable repository, executable, database, or running MCP observation"},
		{name: "unavailable executable metadata", edit: func(r *Report) { r.Installed.Executable.MetadataError = "not a Go executable" }, stage: "unknown", action: "obtain the unavailable repository, executable, database, or running MCP observation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := baseReport()
			if test.edit != nil {
				test.edit(&report)
			}
			derive(&report)
			if report.Stage != test.stage || report.RecommendedNextAction != test.action {
				t.Fatalf("stage/action = %q / %q, want %q / %q; gates=%v", report.Stage, report.RecommendedNextAction, test.stage, test.action, report.UnmetGates)
			}
		})
	}
}

func TestSourceVerificationCannotSubstituteForLiveProbe(t *testing.T) {
	report := baseReport()
	report.Source.Verification = []Verification{{Name: "go test ./...", Status: "reported_pass", Authority: "caller_reported"}}
	report.Probes = []Verification{}
	derive(&report)
	if report.Stage != "identity_current" {
		t.Fatalf("source-only verification produced stage %q", report.Stage)
	}
}

func TestLiveProbeCannotSubstituteForSourceVerification(t *testing.T) {
	report := baseReport()
	report.Source.Verification = []Verification{}
	derive(&report)
	if report.Stage != "identity_current" || report.RecommendedNextAction != "supply attributable source-verification references for the intended commit" {
		t.Fatalf("missing source verification produced %q / %q", report.Stage, report.RecommendedNextAction)
	}
}

func TestFailedProbeDoesNotProduceOperationalVerification(t *testing.T) {
	report := baseReport()
	report.Probes[0].Status = "failed"
	derive(&report)
	if report.Stage != "identity_current" {
		t.Fatalf("failed probe produced stage %q", report.Stage)
	}
}

func TestReadinessInputIsBounded(t *testing.T) {
	service := Service{}
	_, err := service.Report(t.Context(), Input{Probes: make([]string, MaxProbes+1)})
	if err == nil {
		t.Fatal("expected oversized probe list to fail")
	}
}
