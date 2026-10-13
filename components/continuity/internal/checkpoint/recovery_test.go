package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/lease"
)

func TestRecoveryStoreStagesRedactedEnvelopeAndDeduplicatesRequest(t *testing.T) {
	store := RecoveryStore{StateDir: t.TempDir(), ProjectID: "project-one"}
	secret := "fixture-sensitive-value"
	candidate := Checkpoint{
		TaskID: "recover-task", RequestKey: "request-one", Task: "Recover token=" + secret,
		Status: "in_progress", Summary: "password=" + secret, NextAction: "Apply after restart",
		Fence: lease.Fence{Claim: lease.Claim{Holder: "agent", SessionID: "session-one", Token: 7}},
	}
	reconciliation := ActionReconciliation{Updates: []ActionUpdate{{ID: "action-one", Status: "completed", Reason: "token=" + secret}}}
	first, err := store.Stage(candidate, reconciliation, 102, 107)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Stage(candidate, reconciliation, 102, 107)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotent stage got %q, want %q", second.ID, first.ID)
	}
	body, err := os.ReadFile(store.path(first.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) || !strings.Contains(string(body), "[REDACTED]") {
		t.Fatalf("recovery envelope was not redacted: %s", body)
	}
	info, err := os.Stat(store.path(first.ID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("recovery mode = %o, want 600", info.Mode().Perm())
	}
	loaded, err := store.Get(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Candidate.RequestKey != "request-one" || loaded.Candidate.Fence.Claim.Token != 7 {
		t.Fatalf("recovery-only fields were not restored: %#v", loaded)
	}
}

func TestRecoveryStoreRejectsChangedRequestAndTampering(t *testing.T) {
	store := RecoveryStore{StateDir: t.TempDir(), ProjectID: "project-one"}
	candidate := Checkpoint{TaskID: "recover-task", RequestKey: "request-one", Task: "Recover", Status: "in_progress", NextAction: "Apply"}
	staged, err := store.Stage(candidate, ActionReconciliation{}, 102, 107)
	if err != nil {
		t.Fatal(err)
	}
	changed := candidate
	changed.Summary = "different"
	if _, err := store.Stage(changed, ActionReconciliation{}, 102, 107); err == nil || !strings.Contains(err.Error(), "different staged content") {
		t.Fatalf("changed request error = %v", err)
	}
	path := store.path(staged.ID)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"task": "Recover"`, `"task": "Tampered"`, 1))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(staged.ID); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("tamper error = %v", err)
	}
}

func TestRecoveryStoreListAndRemove(t *testing.T) {
	store := RecoveryStore{StateDir: t.TempDir(), ProjectID: "project-one"}
	staged, err := store.Stage(Checkpoint{TaskID: "recover-task", Task: "Recover", Status: "paused", NextAction: "Apply"}, ActionReconciliation{}, 102, 107)
	if err != nil {
		t.Fatal(err)
	}
	values, err := store.List()
	if err != nil || len(values) != 1 || values[0].ID != staged.ID {
		t.Fatalf("list = %#v, %v", values, err)
	}
	if err := store.Remove(staged.ID); err != nil {
		t.Fatal(err)
	}
	values, err = store.List()
	if err != nil || len(values) != 0 {
		t.Fatalf("list after remove = %#v, %v", values, err)
	}
	if _, err := store.Get(filepath.Base("../escape")); err == nil {
		t.Fatal("invalid recovery ID was accepted")
	}
}
