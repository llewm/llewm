package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/updatecheck"
)

func TestUpdateEvidenceWorksWithoutProjectOrReceipt(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	cmd := newRootCommand("llewm")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"update", "evidence", "--commit", strings.Repeat("a", 40), "--target", "windows/amd64", "--output", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var r updatecheck.EvidenceReport
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.State != "missing" || r.Target != "windows/amd64" {
		t.Fatalf("%s: %v", out.String(), err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("evidence created project state: %v %v", entries, err)
	}
}
