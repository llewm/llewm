package checkpoint

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBranchesRecordedBesideContextAndTaskTips(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	create := func(value Checkpoint) Checkpoint {
		t.Helper()
		value.ProjectID, value.NextAction = "p", "Continue"
		if value.Status == "" {
			value.Status = "in_progress"
		}
		created, err := s.Create(ctx, value)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	onFeature := create(Checkpoint{Task: "Feature", TaskID: "task-a", CodeHeads: []CodeHead{{Member: "lib", Commit: commit, Branch: "feature/x"}, {Commit: commit}}})
	noHeads := create(Checkpoint{Task: "Loose", TaskID: "task-loose"})
	onMain := create(Checkpoint{Task: "Main work", TaskID: "task-b", Status: "blocked", CodeHeads: []CodeHead{{Commit: commit, Branch: "main"}}})

	got, err := GetBranches(ctx, s.DB, "p", onFeature.ID)
	if err != nil || got == nil || len(got.Branches) != 2 || got.Branches[0] != (CodeBranch{}) || got.Branches[1] != (CodeBranch{Member: "lib", Branch: "feature/x"}) {
		t.Fatalf("branches of %s: %#v %v", onFeature.ID, got, err)
	}
	// The context keeps its bytes: branches are not part of the code heads JSON.
	var heads string
	if err := s.DB.QueryRow(`SELECT code_heads_json FROM checkpoint_contexts WHERE checkpoint_id=?`, onFeature.ID).Scan(&heads); err != nil || strings.Contains(heads, "branch") {
		t.Fatalf("context heads carry branches: %s %v", heads, err)
	}
	if none, err := GetBranches(ctx, s.DB, "p", noHeads.ID); none != nil || err != nil {
		t.Fatalf("checkpoint without heads has branches: %#v %v", none, err)
	}
	if _, err := s.DB.Exec(`DELETE FROM checkpoint_branches`); err == nil {
		t.Fatal("checkpoint branches are not append-only")
	}

	later := create(Checkpoint{Task: "Feature", TaskID: "task-a", Status: "paused", PredecessorCheckpointID: onFeature.ID})
	tips, err := TaskTips(ctx, s.DB, "p")
	if err != nil || len(tips) != 3 {
		t.Fatalf("tips: %#v %v", tips, err)
	}
	if tips[0].TaskID != "task-a" || tips[0].CheckpointID != later.ID || tips[0].Status != "paused" || len(tips[0].Branches) != 0 {
		t.Fatalf("newest tip: %#v", tips[0])
	}
	if tips[1].CheckpointID != onMain.ID || !tips[1].OnBranch("", "main") || tips[1].OnBranch("", "feature/x") || tips[1].OnBranch("lib", "main") {
		t.Fatalf("second tip: %#v", tips[1])
	}
}

func TestBranchesValidation(t *testing.T) {
	valid := Branches{CheckpointID: "c", Branches: []CodeBranch{{Branch: "main"}}, Authority: "machine_observed", ObservedAt: time.Now(), SchemaVersion: 1}
	if _, err := ValidateBranches(valid); err != nil {
		t.Fatal(err)
	}
	detached := valid
	detached.Branches = []CodeBranch{{}}
	if _, err := ValidateBranches(detached); err != nil {
		t.Fatalf("detached HEAD refused: %v", err)
	}
	for name, change := range map[string]func(*Branches){
		"empty":            func(b *Branches) { b.Branches = nil },
		"space":            func(b *Branches) { b.Branches[0].Branch = "my branch" },
		"control":          func(b *Branches) { b.Branches[0].Branch = "a\x01b" },
		"dots":             func(b *Branches) { b.Branches[0].Branch = "a..b" },
		"long":             func(b *Branches) { b.Branches[0].Branch = strings.Repeat("b", MaxBranchBytes+1) },
		"duplicate member": func(b *Branches) { b.Branches = append(b.Branches, b.Branches[0]) },
		"escaping member":  func(b *Branches) { b.Branches[0].Member = "../other" },
		"asserted":         func(b *Branches) { b.Authority = "caller_asserted" },
	} {
		value := valid
		value.Branches = append([]CodeBranch{}, valid.Branches...)
		change(&value)
		if _, err := ValidateBranches(value); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
