package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunReportsStructuredViolation(t *testing.T) {
	policy := writeFixture(t, "policy.json", `{"schema_version":1,"action_type":"git_commit","policy_version":"v1","effect":"allow","prohibited_trailers":["Co-authored-by"],"require_git_note":true}`)
	message := writeFixture(t, "message", "Subject\n\nCo-authored-by: Agent\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"validate-commit", "--policy", policy, "--message", message, "--json"}, &stdout, &stderr)
	if code != 1 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"code": "prohibited_trailer"`) || !strings.Contains(stdout.String(), `"require_git_note": true`) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunDistinguishesValidAndInputError(t *testing.T) {
	policy := writeFixture(t, "policy.json", `{"schema_version":1,"action_type":"git_commit","policy_version":"v1","effect":"allow","prohibited_trailers":[]}`)
	message := writeFixture(t, "message", "Subject\n")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"validate-commit", "--policy", policy, "--message", message}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "valid") {
		t.Fatalf("valid code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"validate-commit", "--policy", "missing", "--message", message}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "no such file") {
		t.Fatalf("error code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}
