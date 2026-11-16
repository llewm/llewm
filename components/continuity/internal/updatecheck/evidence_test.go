package updatecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func completeEvidence() VerificationReceipt {
	r := VerificationReceipt{SchemaVersion: 1, Repository: "https://github.com/llewm/llewm", Commit: strings.Repeat("a", 40), Target: "windows/amd64", GoVersion: "go1.25.0", RunURL: "https://github.com/llewm/llewm/actions/runs/123/attempts/1", Status: "passed"}
	for _, component := range []string{"continuity", "action-control", "orchestration", "privatestorage"} {
		for _, name := range []string{"test", "vet", "fmt", "build", "standalone-build"} {
			r.Checks = append(r.Checks, VerificationCheck{Component: "components/" + component, Name: name, Status: "passed"})
		}
	}
	r.Checks = append(r.Checks, VerificationCheck{Component: "components/continuity", Name: "python-tests", Status: "passed"})
	return r
}

func writeEvidence(t *testing.T, r VerificationReceipt) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEvidenceRequiresExactIdentityAndCompleteResults(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		edit        func(*VerificationReceipt)
	}{
		{"complete", "passed", func(r *VerificationReceipt) {}},
		{"different commit", "mismatch", func(r *VerificationReceipt) { r.Commit = strings.Repeat("b", 40) }},
		{"different platform", "mismatch", func(r *VerificationReceipt) { r.Target = "linux/amd64" }},
		{"different repository", "mismatch", func(r *VerificationReceipt) { r.Repository = "https://github.com/other/fork" }},
		{"short commit", "invalid", func(r *VerificationReceipt) { r.Commit = "aaaaaaa" }},
		{"missing Go version", "invalid", func(r *VerificationReceipt) { r.GoVersion = "" }},
		{"missing run URL", "invalid", func(r *VerificationReceipt) { r.RunURL = "" }},
		{"credential URL", "invalid", func(r *VerificationReceipt) { r.RunURL = "https://secret@example.org/run" }},
		{"new schema", "invalid", func(r *VerificationReceipt) { r.SchemaVersion++ }},
		{"missing test", "incomplete", func(r *VerificationReceipt) { r.Checks = r.Checks[1:] }},
		{"empty checks", "incomplete", func(r *VerificationReceipt) { r.Checks = nil }},
		{"failed test overrides summary", "failed", func(r *VerificationReceipt) { r.Checks[0].Status = "failed" }},
		{"failed summary", "failed", func(r *VerificationReceipt) { r.Status = "failed" }},
		{"pending test", "pending", func(r *VerificationReceipt) { r.Checks[0].Status = "pending" }},
		{"pending summary", "pending", func(r *VerificationReceipt) { r.Status = "pending" }},
		{"skipped test", "pending", func(r *VerificationReceipt) { r.Checks[0].Status = "skipped" }},
		{"unknown status", "invalid", func(r *VerificationReceipt) { r.Status = "success" }},
		{"duplicate check", "invalid", func(r *VerificationReceipt) { r.Checks = append(r.Checks, r.Checks[0]) }},
		{"unknown check", "invalid", func(r *VerificationReceipt) { r.Checks[0].Name = "anything" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := completeEvidence()
			tc.edit(&r)
			report, err := Evidence(writeEvidence(t, r), strings.Repeat("a", 40), "windows/amd64", "https://github.com/llewm/llewm", "")
			if err != nil || report.State != tc.state || report.PublisherAuthentication != "not_verified" {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}
}

func TestEvidenceMissingMalformedAndBundle(t *testing.T) {
	r := completeEvidence()
	check := func(path, artifact, state string) {
		t.Helper()
		report, err := Evidence(path, r.Commit, r.Target, r.Repository, artifact)
		if err != nil || report.State != state {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		if state == "passed" && artifact != "" && !report.ArtifactVerified {
			t.Fatal("bundle comparison was not recorded")
		}
	}
	check("", "", "missing")
	check(filepath.Join(t.TempDir(), "absent"), "", "missing")
	check(t.TempDir(), "", "unavailable")
	path := filepath.Join(t.TempDir(), "invalid")
	for _, body := range []string{"{", strings.Repeat(" ", (1<<20)+1)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		check(path, "", "invalid")
	}
	bundle := filepath.Join(t.TempDir(), "bundle.zip")
	body := []byte("bundle fixture")
	if err := os.WriteFile(bundle, body, 0600); err != nil {
		t.Fatal(err)
	}
	check(writeEvidence(t, r), bundle, "missing")
	r.Status = "failed"
	check(writeEvidence(t, r), bundle, "failed")
	r.Status = "pending"
	check(writeEvidence(t, r), bundle, "pending")
	r.Status = "passed"
	hash := sha256.Sum256(body)
	r.Artifact = &Asset{Kind: "binary-bundle", Target: r.Target, Size: int64(len(body)), SHA256: hex.EncodeToString(hash[:])}
	check(writeEvidence(t, r), bundle, "passed")
	r.Artifact.Size++
	check(writeEvidence(t, r), bundle, "mismatch")
	r.Artifact.Size--
	r.Artifact.SHA256 = strings.Repeat("b", 64)
	check(writeEvidence(t, r), bundle, "mismatch")
	r.Artifact.Target = "linux/amd64"
	check(writeEvidence(t, r), bundle, "invalid")
	if _, err := Evidence("", "short", r.Target, r.Repository, ""); err == nil {
		t.Fatal("accepted abbreviated selection")
	}
}
