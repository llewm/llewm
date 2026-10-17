package adoption

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiptRejectsStaleTransitionsAndRetainsBaseline(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	if err := os.WriteFile(filepath.Join(root, "PLAN.md"), []byte("token=private-value\nPlan work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Start(dir, root, []string{"PLAN.md"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.Baseline[0].Text, "private-value") {
		t.Fatal("secret persisted")
	}
	s.History = append(s.History, Transition{State: "adopted", At: time.Now().UTC()})
	if err = Save(dir, *s); err != nil {
		t.Fatal(err)
	}
	stale.History = append(stale.History, Transition{State: "exited", At: time.Now().UTC()})
	if err = Save(dir, *stale); err == nil {
		t.Fatal("stale transition accepted")
	}
	s, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Baseline[0].Text = "replaced"
	s.History = append(s.History, Transition{State: "exited", At: time.Now().UTC()})
	if err = Save(dir, *s); err == nil {
		t.Fatal("baseline replacement accepted")
	}
}
func TestSnapshotRefusesSymlinksEscapeAndStatePaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skip(err)
	}
	for _, p := range []string{"../escape", ".git/config", ".llewm/continuity/config.toml", ".continuity/config.toml", "linked/prose.md"} {
		if _, err := Snapshot(root, []string{p}, true); err == nil {
			t.Fatalf("accepted %s", p)
		}
	}
}
