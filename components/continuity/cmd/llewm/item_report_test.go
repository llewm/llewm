package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/workitem"
)

func TestItemReportCLI(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "item", "add", "--id", "report-bug", "--kind", "bug", "--title", "Bootstrap failed", "--body", "Investigate", "--observed", "No bootstrap", "--reproduction", "Open a fresh session", "--environment", "Linux", "--severity", "high")
	var report workitem.BugReport
	output := runCLI(t, "", "item", "report", "work_item:report-bug", "--expected", "Bootstrap token=fixture-report-secret", "--hypothesis", "Hook trust", "--output", "json")
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.ReportID == "" || len(report.Missing) != 0 || strings.Contains(output, "fixture-report-secret") {
		t.Fatalf("bad report: %s", output)
	}
	text := runCLI(t, "", "item", "report", "report-bug")
	if !strings.Contains(text, "Missing: expected") || !strings.Contains(text, "Preparation neither submits") {
		t.Fatalf("bad preview: %s", text)
	}
}
