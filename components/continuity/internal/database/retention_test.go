package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanBackupRetentionKeepsRecentPinnedAndManualBackups(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	writeBackupFixture := func(name string, age time.Duration) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		modified := now.Add(-age)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
		return path
	}
	newest := writeBackupFixture("continuity-newest.db", 2*time.Hour)
	recent := writeBackupFixture("continuity-recent.db", 48*time.Hour)
	pinned := writeBackupFixture("continuity-pinned.db", 90*24*time.Hour)
	expired := writeBackupFixture("continuity-expired.db", 60*24*time.Hour)
	manual := writeBackupFixture("release-baseline.db", 365*24*time.Hour)

	plan, err := PlanBackupRetention(BackupRetentionOptions{Dir: dir, KeepLast: 1, KeepDays: 7, Pinned: []string{filepath.Base(pinned)}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || len(plan.PruneCandidates) != 1 || plan.PruneCandidates[0] != expired || plan.ReclaimBytes != int64(len("continuity-expired.db")) {
		t.Fatalf("retention plan: %#v", plan)
	}
	wantReasons := map[string]string{newest: "keep_last", recent: "keep_days", pinned: "pinned", expired: "expired", manual: "manual_name"}
	for _, entry := range plan.Entries {
		if want, ok := wantReasons[entry.Path]; !ok || entry.Reason != want {
			t.Fatalf("entry %s reason %q, want %q", entry.Path, entry.Reason, want)
		}
		delete(wantReasons, entry.Path)
	}
	if len(wantReasons) != 0 {
		t.Fatalf("missing retention entries: %#v", wantReasons)
	}
	for _, path := range []string{newest, recent, pinned, expired, manual} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dry-run changed %s: %v", path, err)
		}
	}
}

func TestPlanBackupRetentionRejectsNegativePolicy(t *testing.T) {
	if _, err := PlanBackupRetention(BackupRetentionOptions{Dir: t.TempDir(), KeepLast: -1}); err == nil {
		t.Fatal("negative keep_last was accepted")
	}
	if _, err := PlanBackupRetention(BackupRetentionOptions{Dir: t.TempDir(), KeepDays: -1}); err == nil {
		t.Fatal("negative keep_days was accepted")
	}
}

func TestApplyBackupRetentionRemovesOnlyReviewedCandidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "continuity-old.db")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanBackupRetention(BackupRetentionOptions{Dir: dir, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ApplyBackupRetention(plan, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Applied || len(receipt.Removed) != 1 || receipt.Removed[0] != path || receipt.ReclaimedBytes != 3 {
		t.Fatalf("prune receipt: %#v", receipt)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("candidate survived apply: %v", err)
	}
}

func TestApplyBackupRetentionRefusesChangedOrEscapingCandidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "continuity-old.db")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanBackupRetention(BackupRetentionOptions{Dir: dir, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBackupRetention(plan, time.Now()); err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("changed backup error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("changed backup was removed: %v", err)
	}

	escaping := plan
	escaping.PruneCandidates = []string{filepath.Join(dir, "..", "outside.db")}
	if _, err := ApplyBackupRetention(escaping, time.Now()); err == nil || !strings.Contains(err.Error(), "escapes backup directory") {
		t.Fatalf("escaping backup error = %v", err)
	}
}
