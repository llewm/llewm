package checkpoint

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestContextRecordsWhatACheckpointContinued(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	create := func(value Checkpoint) Checkpoint {
		t.Helper()
		value.ProjectID, value.Status, value.NextAction = "p", "in_progress", "Continue"
		created, err := s.Create(ctx, value)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	first := create(Checkpoint{Task: "Work", TaskID: "task-a", CodeHeads: []CodeHead{{Member: "b", Commit: commit}, {Commit: strings.Repeat("b", 40)}}})
	second := create(Checkpoint{Task: "Work", TaskID: "task-a", PredecessorCheckpointID: first.ID})
	explicit := create(Checkpoint{Task: "Work", TaskID: "task-a", PredecessorCheckpointID: second.ID})
	untasked := create(Checkpoint{Task: "Loose", TaskID: "task-loose"})

	for _, want := range []struct {
		id, basedOn string
		heads       int
	}{{first.ID, "", 2}, {second.ID, first.ID, 0}, {explicit.ID, second.ID, 0}, {untasked.ID, "", 0}} {
		got, err := GetContext(ctx, s.DB, "p", want.id)
		if err != nil || got == nil || got.BasedOnCheckpoint != want.basedOn || len(got.CodeHeads) != want.heads || got.Authority != "machine_observed" {
			t.Fatalf("context of %s: %#v %v, want based on %q with %d heads", want.id, got, err, want.basedOn, want.heads)
		}
	}
	// Heads are stored in member order, the primary repository first.
	got, _ := GetContext(ctx, s.DB, "p", first.ID)
	if got.CodeHeads[0].Member != "" || got.CodeHeads[1].Member != "b" {
		t.Fatalf("heads not canonical: %#v", got.CodeHeads)
	}
	if missing, err := GetContext(ctx, s.DB, "p", "checkpoint:none"); missing != nil || err != nil {
		t.Fatalf("missing context: %#v %v", missing, err)
	}
	all, err := AllContexts(ctx, s.DB, "p")
	if err != nil || len(all) != 4 {
		t.Fatalf("all contexts: %d %v", len(all), err)
	}
	if _, err := s.DB.Exec(`UPDATE checkpoint_contexts SET based_on_checkpoint_id=NULL`); err == nil {
		t.Fatal("checkpoint contexts are not append-only")
	}
}

func TestContextValidation(t *testing.T) {
	valid := Context{CheckpointID: "c", CodeHeads: []CodeHead{{Commit: strings.Repeat("a", 40)}}, Authority: "machine_observed", ObservedAt: time.Now(), SchemaVersion: 1}
	if _, err := ValidateContext(valid); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Context){
		"short commit":     func(c *Context) { c.CodeHeads[0].Commit = "abc123" },
		"uppercase commit": func(c *Context) { c.CodeHeads[0].Commit = strings.Repeat("A", 40) },
		"duplicate member": func(c *Context) { c.CodeHeads = append(c.CodeHeads, c.CodeHeads[0]) },
		"escaping member":  func(c *Context) { c.CodeHeads[0].Member = "../other" },
		"self based":       func(c *Context) { c.BasedOnCheckpoint = "c" },
		"asserted":         func(c *Context) { c.Authority = "caller_asserted" },
	} {
		value := valid
		value.CodeHeads = append([]CodeHead{}, valid.CodeHeads...)
		change(&value)
		if _, err := ValidateContext(value); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
