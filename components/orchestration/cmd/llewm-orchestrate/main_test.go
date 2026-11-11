package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/privatestorage"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
)

func TestCLIManualWorkflowAndPrivateCredentials(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "private", "team.db")
	tokenPath := filepath.Join(dir, "dave.token")
	invoke := func(args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		if err := run(args, &out, &stderr); err != nil {
			t.Fatalf("%v: %v %s", args, err, stderr.String())
		}
		return out.String()
	}
	invoke("init", "--db", db)
	output := invoke("member", "--db", db, "--id", "dave", "--permissions", "plan,accept,assign,review,work", "--token-file", tokenPath)
	if strings.Contains(output, "lworch_") {
		t.Fatal("credential printed")
	}
	f, err := privatestorage.Open(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := privatestorage.CheckFile(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s, err := coordination.Open(db, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := httptest.NewServer(transport.Handler(s))
	defer server.Close()
	c := coordination.Command{SchemaVersion: 1, RequestID: "propose", PlanID: "plan", Action: "propose", Definition: &coordination.Definition{Title: "CLI plan", Items: []coordination.Item{{ID: "item", Title: "Work", AcceptanceCriteria: "Integration verified"}}}}
	commandPath := filepath.Join(dir, "command.json")
	request := func(c coordination.Command) coordination.Receipt {
		t.Helper()
		body, _ := json.Marshal(c)
		if err := os.WriteFile(commandPath, body, 0600); err != nil {
			t.Fatal(err)
		}
		output := invoke("request", "--endpoint", server.URL, "--token-file", tokenPath, "--file", commandPath)
		var r coordination.Receipt
		if err := json.Unmarshal([]byte(output), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	request(c)
	request(coordination.Command{SchemaVersion: 1, RequestID: "accept", PlanID: "plan", Action: "accept-plan", BaseVersion: 1, Revision: 1})
	claim := request(coordination.Command{SchemaVersion: 1, RequestID: "claim", PlanID: "plan", Action: "claim", BaseVersion: 2, ItemID: "item", Machine: "laptop", Agent: "codex"})
	result := request(coordination.Command{SchemaVersion: 1, RequestID: "submit", PlanID: "plan", Action: "submit", AttemptID: claim.AttemptID, Kind: "result", Context: "Implemented and tested", ArtifactRefs: []string{"commit:abc"}, VerificationRefs: []string{"test-run:1"}})
	request(coordination.Command{SchemaVersion: 1, RequestID: "review", PlanID: "plan", Action: "review", BaseVersion: 4, SubmissionID: result.SubmissionID, Decision: "accept", Reason: "Integrated and verified", VerificationRefs: []string{"integration:1"}})
	view := invoke("show", "--endpoint", server.URL, "--token-file", tokenPath, "--plan", "plan")
	if !strings.Contains(view, result.SubmissionID) {
		t.Fatal("completion not readable")
	}
	if !strings.Contains(invoke("list", "--endpoint", server.URL, "--token-file", tokenPath), "CLI plan") {
		t.Fatal("plan not listed")
	}
	history := invoke("show", "--endpoint", server.URL, "--token-file", tokenPath, "--plan", "plan", "--history", "--after", "1")
	var events coordination.History
	if err := json.Unmarshal([]byte(history), &events); err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 4 || events.Events[0].Sequence != 2 {
		t.Fatal("CLI history cursor failed")
	}
	setTokenPrivate(t, tokenPath, false)
	if _, err := readPrivateToken(tokenPath); err == nil {
		t.Fatal("world-readable token accepted")
	}
	setTokenPrivate(t, tokenPath, true)
	invoke("revoke", "--db", db, "--id", "dave")
	token, err := readPrivateToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.List(context.Background(), token); err == nil {
		t.Fatal("revoked engineer still authenticated")
	}
}

func TestServeRejectsPlaintextPublicListener(t *testing.T) {
	db := filepath.Join(t.TempDir(), "private", "team.db")
	var output bytes.Buffer
	if err := run([]string{"init", "--db", db}, &output, &output); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"serve", "--db", db, "--listen", "0.0.0.0:9467"}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "require TLS") {
		t.Fatal(err)
	}
}

func TestCredentialImportCreatesPrivateNonOverwritingFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "received.token")
	target := filepath.Join(dir, "private", "worker.token")
	token := "lworch_" + strings.Repeat("a", 64)
	if err := os.WriteFile(source, []byte(token+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{"credential-import", "--file", source, "--token-file", target}
	if err := run(args, &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), token) {
		t.Fatal("credential printed")
	}
	if got, err := readPrivateToken(target); err != nil || got != token {
		t.Fatalf("private import failed: %v", err)
	}
	if err := run(args, &out, &out); err == nil {
		t.Fatal("existing private credential overwritten")
	}
	for _, invalid := range []string{"wrong", token + strings.Repeat(" ", 300), "lworch_" + strings.Repeat("z", 64)} {
		if err := os.WriteFile(source, []byte(invalid), 0644); err != nil {
			t.Fatal(err)
		}
		if err := run([]string{"credential-import", "--file", source, "--token-file", filepath.Join(dir, "private", "invalid.token")}, &out, &out); err == nil {
			t.Fatal("invalid credential imported")
		}
	}
}
