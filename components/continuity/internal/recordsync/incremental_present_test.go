package recordsync

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/decision"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/resource"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

// Qualify complete local arrivals and remote appends through proof renewal,
// changing revision dependencies and checkpoint action history.
func TestMixedHistoryMatchesFullImporter(t *testing.T) {
	for _, mode := range []string{"remote", "present", "remote-local-drift"} {
		present := mode == "present"
		t.Run(mode, func(t *testing.T) {
			ctx, source, sourceID := indexedTestProject(t, "present-source")
			_, indexed, indexedID := indexedTestProject(t, "present-indexed")
			_, full, fullID := indexedTestProject(t, "present-full")
			plans := trackplan.Service{DB: source}
			plan := trackplan.Plan{ProjectID: sourceID, ID: "plan", RoadmapNodeID: "node", TaskID: "task", Title: "Qualification", Summary: "Initial qualification", Status: "active", Creator: "test", Items: []trackplan.Item{{ID: "item", Position: 1, Title: "Qualify", Status: "active"}}}
			if _, err := plans.AppendPlan(ctx, plan); err != nil {
				t.Fatal(err)
			}
			index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
			previous := ""
			for cycle := 0; cycle < 4; cycle++ {
				if cycle > 0 {
					hostID := fmt.Sprintf("host-%d", cycle)
					if cycle == 3 {
						// The old note refers to this ID. The typed closure
						// must include it when a resource revision arrives.
						hostID = "host-2"
					}
					plan.Summary = fmt.Sprintf("Progress %d", cycle)
					if _, err := plans.AppendPlan(ctx, plan); err != nil {
						t.Fatal(err)
					}
					if _, err := (resource.Service{DB: source}).Record(ctx, resource.Resource{ProjectID: sourceID, ID: hostID, Kind: "host", Title: "Fixture", Summary: plan.Summary, Creator: "test"}); err != nil {
						t.Fatal(err)
					}
					if _, err := (resource.Service{DB: source}).AddNote(ctx, resource.Note{ProjectID: sourceID, ResourceID: hostID, Text: plan.Summary, Authority: "machine_observed", Creator: "test"}); err != nil {
						t.Fatal(err)
					}
					saved, err := (checkpoint.Service{DB: source}).CreateWithActions(ctx, checkpoint.Checkpoint{ID: fmt.Sprintf("checkpoint-%d", cycle), ProjectID: sourceID, TaskID: "task", PredecessorCheckpointID: previous, Task: "Qualify", Summary: plan.Summary, Status: "in_progress", NextAction: "Continue", CodeHeads: []checkpoint.CodeHead{{Commit: "0123456789abcdef0123456789abcdef01234567", Branch: "main"}}})
					if err != nil {
						t.Fatal(err)
					}
					previous = saved.Checkpoint.ID
				}
				export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
				if err != nil {
					t.Fatal(err)
				}
				block, err := EncodeBlock(lineage, export.Envelopes)
				if err != nil {
					t.Fatal(err)
				}
				if cycle > 0 && present {
					for _, target := range []Importer{{DB: indexed, ProjectID: indexedID}, {DB: full, ProjectID: fullID}} {
						if _, err := target.Import(ctx, export.Envelopes); err != nil {
							t.Fatal(err)
						}
					}
				}
				drift := mode == "remote-local-drift" && cycle == 2
				if drift {
					if _, err := (resource.Service{DB: indexed}).Record(ctx, resource.Resource{ProjectID: indexedID, ID: "local-drift", Kind: "host", Title: "Local observation", Creator: "test"}); err != nil {
						t.Fatal(err)
					}
					driftExport, err := portable.EncodeMemoryExport(ctx, indexed, indexedID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := (Importer{DB: full, ProjectID: fullID}).Import(ctx, driftExport.Envelopes); err != nil {
						t.Fatal(err)
					}
				}
				want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, export.Envelopes)
				var got ImportResult
				var gotErr error
				if cycle > 0 {
					hash, eligible := mixedReplaySet(export.Envelopes)
					if !eligible {
						t.Fatal("ineligible fixture")
					}
					var used bool
					before, err := portable.LocalEnvelopes(ctx, indexed, indexedID)
					if err != nil {
						t.Fatal(err)
					}
					got, used, gotErr = index.tryMixedAdditions(ctx, hash, export.Envelopes)
					wantUsed := !drift
					if used != wantUsed || gotErr != nil {
						t.Fatalf("cycle %d: shortcut used=%t want=%t err=%v", cycle, used, wantUsed, gotErr)
					}
					if !used {
						after, err := portable.LocalEnvelopes(ctx, indexed, indexedID)
						if err != nil || len(before) != len(after) {
							t.Fatalf("fallback left partial records: %v", err)
						}
						for key, object := range before {
							if !bytes.Equal(object.Bytes, after[key].Bytes) {
								t.Fatalf("fallback changed %s", key)
							}
						}
						got, gotErr = index.ImportBlocks(ctx, []Block{block})
					}
				} else {
					got, gotErr = index.ImportBlocks(ctx, []Block{block})
				}
				if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
					t.Fatalf("cycle %d: full=%+v %v indexed=%+v %v", cycle, want, wantErr, got, gotErr)
				}
				if cycle > 0 && !drift {
					hash, _ := mixedReplaySet(export.Envelopes)
					if _, proof, err := index.mixedReplaySnapshot(ctx, hash, export.Envelopes, ""); err != nil || !proof {
						t.Fatalf("cycle %d: missing renewed proof: %v", cycle, err)
					}
				}
				// Both appends and present additions renew the proof directly;
				// a local-drift fallback requires an unchanged replay.
				for replay := 0; replay < 2; replay++ {
					if _, err := index.ImportBlocks(ctx, []Block{block}); err != nil {
						t.Fatal(err)
					}
				}
				wantRecords, err := portable.LocalEnvelopes(ctx, full, fullID)
				if err != nil {
					t.Fatal(err)
				}
				gotRecords, err := portable.LocalEnvelopes(ctx, indexed, indexedID)
				if err != nil || len(wantRecords) != len(gotRecords) {
					t.Fatalf("state size differs: %d/%d %v", len(wantRecords), len(gotRecords), err)
				}
				for key, object := range wantRecords {
					if !bytes.Equal(object.Bytes, gotRecords[key].Bytes) {
						t.Fatalf("cycle %d: %s differs", cycle, key)
					}
				}
			}
		})
	}
}

// A prior incoming actor can be present only as a raw local row. The smaller
// affected bundle omits it; proof renewal must still compare its exact bytes.
func TestMixedPresentIncludesPriorUnretainedInvocation(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "present-unretained-actor")
	at := time.Unix(1234567890, 0).UTC()
	if _, err := (attribution.Service{DB: db}).AppendInvocation(ctx, attribution.Invocation{ID: "actor", ProjectID: project, ActorType: "agent", IdentitySource: "caller_asserted", OccurredAt: at, ReceivedAt: at}); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	local, err := portable.LocalEnvelopesForImport(ctx, tx, project, []portable.EncodedEnvelope{{Envelope: portable.Envelope{RecordType: "actor_invocation", RecordID: "actor"}}})
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	actor := local[portable.RecordKey("actor_invocation", "actor", 0)]
	if len(actor.Bytes) == 0 {
		t.Fatal("missing actor fixture")
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{actor})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := index.ImportBlocks(ctx, []Block{block}); err != nil {
			t.Fatal(err)
		}
	}
	var retained int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM portable_envelopes WHERE project_id=? AND record_type='actor_invocation'", project).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("actor retained=%d: %v", retained, err)
	}
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "arrival", Title: "Arrival", Rationale: "Observation", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	export, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	objects := append(export.Envelopes, actor)
	hash, ok := mixedReplaySet(objects)
	if !ok {
		t.Fatal("ineligible fixture")
	}
	got, used, err := index.tryMixedAdditions(ctx, hash, objects)
	if err != nil || !used {
		t.Fatalf("present renewal used=%t result=%+v: %v", used, got, err)
	}
	if _, proof, err := index.mixedReplaySnapshot(ctx, hash, objects, ""); err != nil || !proof {
		t.Fatalf("missing renewed proof: %v", err)
	}
}
