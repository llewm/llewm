package memsync

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestLeaseRefreshBatchesMissesAndReusesExactTips(t *testing.T) {
	refs, _, event := leaseHistoryFixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		event.ScopeID, event.GrantID = fmt.Sprintf("scope-%d", i), fmt.Sprintf("grant-%d", i)
		if _, err := refs.Write(ctx, "", event); err != nil {
			t.Fatal(err)
		}
	}
	read := func(reads, processes, hits int64) map[string]lease.RemoteScope {
		t.Helper()
		ctx, work := syncwork.WithWorkMetrics(context.Background())
		scopes, err := refs.Scopes(ctx)
		if err != nil || len(scopes) != 3 {
			t.Fatalf("refresh: %v %v", scopes, err)
		}
		got := work.Counts()
		if got["lease_history_git_reads"] != reads || got["lease_commit_batch_processes"] != processes || got["lease_history_cache_hits"] != hits {
			t.Fatalf("wrong refresh work: %v", got)
		}
		return scopes
	}
	read(3, 1, 0)
	old := read(0, 0, 3)
	event.Sequence, event.Action = 2, "renew"
	previous := old[event.Scope()].Version
	if _, err := refs.Write(ctx, previous, event); err != nil {
		t.Fatal(err)
	}
	changed := read(2, 1, 2)
	if len(changed[event.Scope()].Events) != 2 || changed[event.Scope()].Version == previous {
		t.Fatal("changed tip hidden by cache")
	}
	read(0, 0, 3)
	event.RecordedAt = event.RecordedAt.Add(time.Second)
	if _, err := refs.Write(ctx, previous, event); err != lease.ErrRemoteMoved {
		t.Fatalf("warm cache weakened CAS: %v", err)
	}
}

func TestGitCommitBatchValidationAndCleanup(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			transport := objectTransportFixture(t, format)
			transport.repo.Identity = []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}
			width := 40
			if format == "sha256" {
				width = 64
			}
			tree := objectTree(t, transport, nil, width)
			oid, err := transport.writeCommit(ctx, tree, nil, "batch fixture\n")
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{tree, strings.Repeat("f", width), "--help"} {
				batch := &gitCommitBatch{repo: transport.repo}
				body, err := batch.Read(ctx, oid)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := decodeTransportCommit(oid, body); err != nil {
					t.Fatal(err)
				}
				if _, err := batch.Read(ctx, bad); err == nil {
					t.Fatalf("invalid batch object %s accepted", bad)
				}
				batch.Close()
				batch.Close()
				if batch.cmd.ProcessState == nil {
					t.Fatal("Git process not reaped")
				}
				if _, err := batch.Read(ctx, oid); err == nil {
					t.Fatal("closed batch reused")
				}
			}
			// Cancellation after a successful request must also reap the child.
			bounded, cancel := context.WithCancel(ctx)
			batch := &gitCommitBatch{repo: transport.repo}
			if _, err := batch.Read(bounded, oid); err != nil {
				t.Fatal(err)
			}
			cancel()
			if _, err := batch.Read(bounded, oid); err == nil {
				t.Fatal("cancelled batch accepted request")
			}
			batch.Close()
			if batch.cmd.ProcessState == nil {
				t.Fatal("cancelled process not reaped")
			}
			// Use hash-object directly to make a valid commit above the transport
			// size bound; the reader must reject its size before reading the body.
			large := fmt.Sprintf("tree %s\nauthor A <a@x> 0 +0000\ncommitter A <a@x> 0 +0000\n\n", tree) + strings.Repeat("x", maxTransportCommitBytes)
			var result bytes.Buffer
			err = transport.repo.execOut(ctx, strings.NewReader(large), &result, []string{"hash-object", "-t", "commit", "-w", "--stdin"})
			huge := strings.TrimSpace(result.String())
			if err != nil {
				t.Fatal(err)
			}
			batch = &gitCommitBatch{repo: transport.repo}
			if _, err := batch.Read(ctx, huge); err == nil {
				t.Fatal("oversized batch commit accepted")
			}
			batch.Close()
			if batch.cmd.ProcessState == nil {
				t.Fatal("oversized writer not reaped")
			}
		})
	}
}
