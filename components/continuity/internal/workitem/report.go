package workitem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const ReportSchemaVersion = 1
const MaxReportBytes = 32 << 10

// ReportOptions supplies report-only context, never a change to the source item.
type ReportOptions struct {
	Expected   string `json:"expected,omitempty"`
	Hypothesis string `json:"hypothesis,omitempty"`
}

type ReportSource struct {
	ProjectID  string    `json:"project_id"`
	Ref        string    `json:"ref"`
	Creator    string    `json:"creator"`
	Model      string    `json:"model,omitempty"`
	Authority  string    `json:"authority"`
	RecordedAt time.Time `json:"recorded_at"`
}

// ReportDiagnostics deliberately excludes executable paths, environment
// variables, transcripts, repository contents and other database records.
// These describe the reporting runtime, not necessarily the affected runtime.
type ReportDiagnostics struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	BuildDirty    bool   `json:"build_dirty"`
	MCPAPIVersion int    `json:"mcp_api_version"`
	Platform      string `json:"platform"`
	Architecture  string `json:"architecture"`
	GoVersion     string `json:"go_version"`
}

// BugReport is a derived, provider-neutral view of one existing bug. Its hash
// identifies this exact redacted payload, not a verified duplicate or permission.
type BugReport struct {
	SchemaVersion       int               `json:"schema_version"`
	ReportID            string            `json:"report_id"`
	Source              ReportSource      `json:"source"`
	Title               string            `json:"title"`
	Description         string            `json:"description"`
	Observed            string            `json:"observed,omitempty"`
	Expected            string            `json:"expected,omitempty"`
	Reproduction        string            `json:"reproduction,omitempty"`
	Environment         string            `json:"environment,omitempty"`
	Severity            string            `json:"severity,omitempty"`
	Hypothesis          string            `json:"hypothesis,omitempty"`
	SupplementAuthority string            `json:"supplement_authority"`
	ReportingRuntime    ReportDiagnostics `json:"reporting_runtime"`
	Missing             []string          `json:"missing"`
	Disclaimer          string            `json:"disclaimer"`
}

// Report prepares an intact bounded report without persisting or delivering it.
// Missing reproduction or context is reported explicitly, never inferred.
func (s Service) Report(ctx context.Context, projectID, id string, options ReportOptions) (*BugReport, error) {
	item, err := s.Get(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, fmt.Errorf("work item %q not found in this project", id)
	}
	if item.Kind != "bug" {
		return nil, fmt.Errorf("work item %q is %s, not a bug", id, item.Kind)
	}
	if options.Expected, err = clean("expected behavior", options.Expected, false); err != nil {
		return nil, err
	}
	if options.Hypothesis, err = clean("hypothesis", options.Hypothesis, false); err != nil {
		return nil, err
	}
	info := buildinfo.Current()
	report := &BugReport{
		SchemaVersion: ReportSchemaVersion,
		Source:        ReportSource{ProjectID: redact.String(item.ProjectID), Ref: redact.String(Ref(item.ID)), Creator: redact.String(item.Creator), Model: redact.String(item.Model), Authority: item.Authority, RecordedAt: item.RecordedAt},
		Title:         redact.String(item.Title), Description: redact.String(item.Body), Observed: redact.String(item.Observed),
		Expected: options.Expected, Reproduction: redact.String(item.Reproduction), Environment: redact.String(item.Environment),
		Severity: item.Severity, Hypothesis: options.Hypothesis, SupplementAuthority: "caller_asserted",
		ReportingRuntime: ReportDiagnostics{Version: info.ApplicationVersion, Commit: info.GitCommit, BuildDirty: info.BuildDirty, MCPAPIVersion: info.MCPAPIVersion, Platform: runtime.GOOS, Architecture: runtime.GOARCH, GoVersion: info.GoVersion},
		Missing:          []string{},
		Disclaimer:       "Draft report; source claims retain their recorded authority. Expected behavior and hypothesis are report-only caller claims. Runtime diagnostics describe the reporter. Redaction is pattern-based; review before sharing. Preparation neither submits nor grants submission permission.",
	}
	for _, field := range []struct{ name, value string }{{"observed", report.Observed}, {"expected", report.Expected}, {"reproduction", report.Reproduction}, {"environment", report.Environment}, {"severity", report.Severity}} {
		if strings.TrimSpace(field.value) == "" {
			report.Missing = append(report.Missing, field.name)
		}
	}
	body, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(body)
	report.ReportID = "sha256:" + hex.EncodeToString(hash[:])
	// Measure the same indented JSON the CLI writes. Reject instead of silently
	// dropping reproduction steps or provenance to fit the transport budget.
	body, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(body)+1 > MaxReportBytes {
		return nil, fmt.Errorf("bug report exceeds %d bytes; shorten the source bug or report-only context", MaxReportBytes)
	}
	return report, nil
}

func (r BugReport) Markdown() string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\nReport: %s\nSource: %s (%s)\nRecorded by: %s", r.Title, r.ReportID, r.Source.Ref, r.Source.Authority, r.Source.Creator)
	if r.Source.Model != "" {
		fmt.Fprintf(&out, " (%s)", r.Source.Model)
	}
	fmt.Fprintf(&out, "\nRecorded: %s\n", r.Source.RecordedAt.Format(time.RFC3339Nano))
	for _, section := range []struct{ name, value string }{{"Description", r.Description}, {"Observed behavior", r.Observed}, {"Expected behavior (caller claim)", r.Expected}, {"Reproduction", r.Reproduction}, {"Affected environment (source claim)", r.Environment}, {"Severity (source claim)", r.Severity}, {"Hypothesis (unverified)", r.Hypothesis}} {
		value := section.value
		if value == "" {
			value = "Not provided."
		}
		fmt.Fprintf(&out, "\n## %s\n\n%s\n", section.name, value)
	}
	d := r.ReportingRuntime
	fmt.Fprintf(&out, "\n## Reporting runtime\n\n%s %s (dirty=%t), API %d, %s/%s, %s\n", d.Version, d.Commit, d.BuildDirty, d.MCPAPIVersion, d.Platform, d.Architecture, d.GoVersion)
	if len(r.Missing) > 0 {
		fmt.Fprintf(&out, "\nMissing: %s\n", strings.Join(r.Missing, ", "))
	}
	fmt.Fprintf(&out, "\n%s\n", r.Disclaimer)
	return out.String()
}
