package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestPortableMemoryGitReviewedCLIWorkflow(t *testing.T) {
	t.Setenv("CONTINUITY_HOME", "")
	t.Setenv("CONTINUITY_GLOBAL_HOME", t.TempDir())
	sandbox := t.TempDir()
	t.Chdir(sandbox)
	source := qualificationProject(t, sandbox, "git-source")
	target := qualificationProject(t, sandbox, "git-target")
	collision := qualificationProject(t, sandbox, "git-collision")
	lineage := "lineage:34343434343434343434343434343434"
	for _, root := range []string{source, target, collision} {
		runCLI(t, "", "--project-root", root, "init")
		runCLI(t, "", "--project-root", root, "sync", "init", "--lineage", lineage)
	}
	runCLI(t, "", "--project-root", source, "narrative", "record", "--id", "git-reviewed-narrative", "--title", "Reviewed export", "--trigger", "manual", "--claims-json", `[{"kind":"outcome","text":"Private source `+source+` token=git-profile-secret.","authority":"caller_asserted"}]`)
	runCLI(t, "", "--project-root", collision, "narrative", "record", "--id", "git-reviewed-narrative", "--title", "Conflicting reviewed export", "--trigger", "manual", "--claims-json", `[{"kind":"outcome","text":"Same identity with different content.","authority":"caller_asserted"}]`)
	exportRoot := filepath.Join(source, "reviewed-memory")
	if err := os.Mkdir(exportRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	var exported portable.ExportResult
	decodeCLI(t, runCLI(t, "", "--project-root", source, "memory", "export", "--transport", "git-reviewed-v1", "--directory", exportRoot), &exported)
	if exported.TransportKind != portable.GitReviewedTransportKind || !exported.ReviewRequired || exported.GitActionsPerformed != 0 || !strings.HasPrefix(exported.Directory, exportRoot+string(filepath.Separator)) {
		t.Fatalf("unexpected Git-reviewed export result: %#v", exported)
	}
	if output, err := exec.Command("git", "-C", source, "diff", "--cached", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("export changed the Git index: %v: %s", err, output)
	}
	status, err := exec.Command("git", "-C", source, "status", "--porcelain", "--", "reviewed-memory").CombinedOutput()
	if err != nil || string(status) != "?? reviewed-memory/\n" {
		t.Fatalf("export was not left as explicit untracked review material: status=%q err=%v", status, err)
	}
	if err := filepath.WalkDir(exported.Directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(body, []byte(source)) || bytes.Contains(body, []byte("git-profile-secret")) || strings.HasSuffix(path, "continuity.db") || strings.HasSuffix(path, "continuity.db-wal") || strings.HasSuffix(path, "continuity.db-shm") {
			t.Fatalf("Git-reviewed export leaked private or live-store data in %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var inspection portable.BundleInspection
	decodeCLI(t, runCLI(t, "", "memory", "inspect", "--directory", exported.Directory), &inspection)
	if inspection.TransportKind != portable.GitReviewedTransportKind || inspection.RecordCount != 1 || inspection.BodiesLoaded != 0 {
		t.Fatalf("unexpected Git-reviewed inspection: %#v", inspection)
	}
	var plan portable.ImportPlan
	decodeCLI(t, runCLI(t, "", "--project-root", target, "memory", "plan", "--directory", exported.Directory), &plan)
	if !plan.ApplyAllowed || plan.Counts[portable.PlanAppend] != 1 || plan.WritesPerformed != 0 {
		t.Fatalf("unexpected Git-reviewed plan: %#v", plan)
	}
	var applied portable.ImportApplyResult
	decodeCLI(t, runCLI(t, "", "--project-root", target, "memory", "apply", "--directory", exported.Directory, "--confirm-lineage", lineage, "--confirm-project", plan.ProjectID), &applied)
	if applied.Receipt == nil || applied.Receipt.AppendedRecords != 1 || applied.Plan.WritesPerformed != 2 {
		t.Fatalf("unexpected Git-reviewed apply: %#v", applied)
	}
	targetExportRoot := filepath.Join(target, "reviewed-memory")
	if err := os.Mkdir(targetExportRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	var reexported portable.ExportResult
	decodeCLI(t, runCLI(t, "", "--project-root", target, "memory", "export", "--transport", "git-reviewed-v1", "--directory", targetExportRoot), &reexported)
	_, sourceRecords, err := portable.ReadBundleDirectory(exported.Directory)
	if err != nil {
		t.Fatal(err)
	}
	_, targetRecords, err := portable.ReadBundleDirectory(reexported.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Manifest.RecordSetSHA256 != reexported.Manifest.RecordSetSHA256 || !bytes.Equal(sourceRecords, targetRecords) {
		t.Fatal("Git-reviewed recovery changed semantic re-export")
	}
	collisionRowsBefore := qualificationPortableRows(t, collision)
	var collisionPlan portable.ImportPlan
	decodeCLI(t, runCLI(t, "", "--project-root", collision, "memory", "plan", "--directory", exported.Directory), &collisionPlan)
	if collisionPlan.ApplyAllowed || collisionPlan.Counts[portable.PlanCollision] != 1 || collisionPlan.WritesPerformed != 0 {
		t.Fatalf("Git-reviewed collision was not refused during planning: %#v", collisionPlan)
	}
	if err, _ := runCLIError("--project-root", collision, "memory", "apply", "--directory", exported.Directory, "--confirm-lineage", lineage, "--confirm-project", collisionPlan.ProjectID); err == nil || qualificationPortableRows(t, collision) != collisionRowsBefore {
		t.Fatalf("Git-reviewed collision apply did not fail without writes: %v", err)
	}
	if err, _ := runCLIError("--project-root", source, "memory", "export", "--transport", "git-reviewed-v1", "--directory", exportRoot); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("same record set was republished over immutable review material: %v", err)
	}

}
