package memsync

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func leaseHistoryFixture(t testing.TB) (leaseRefs, *objectTransport, lease.RemoteEvent) {
	t.Helper()
	m := newMachine(t, "lease-cache", bareRemote(t))
	repo, err := m.syncer.repository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	refs, err := m.syncer.leaseRefs(context.Background(), repo, true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	event := lease.RemoteEvent{GrantID: "grant", ScopeKind: "task", ScopeID: "build", Token: 1, Holder: "codex", InstanceID: "fixture", AcquiredAt: now, Sequence: 1, Action: "acquire", ExpiresAt: now.Add(time.Hour), RecordedAt: now, SchemaVersion: lease.SchemaVersion}
	return refs, &objectTransport{repo: repo}, event
}

func leaseHistoryCommit(t testing.TB, transport *objectTransport, event lease.RemoteEvent, parents ...string) string {
	t.Helper()
	ctx := context.Background()
	tree, err := transport.writeTree(ctx, nil, 40)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(event)
	oid, err := transport.writeCommit(ctx, tree, parents, string(body)+"\n")
	if err != nil {
		t.Fatal(err)
	}
	return oid
}

func TestLeaseHistoryCacheExactTipAndSafeFallback(t *testing.T) {
	t.Parallel()
	refs, transport, event := leaseHistoryFixture(t)
	first := leaseHistoryCommit(t, transport, event)
	event.Sequence, event.Action = 2, "renew"
	tip := leaseHistoryCommit(t, transport, event, first)
	ref := refs.ref(event.ScopeKind, event.ScopeID)
	read := func(wantReads, wantHits int64) []lease.RemoteEvent {
		t.Helper()
		ctx, metrics := syncwork.WithWorkMetrics(context.Background())
		events, err := refs.cachedEvents(ctx, &objectTransport{repo: refs.repo}, ref, tip)
		if err != nil {
			t.Fatal(err)
		}
		counts := metrics.Counts()
		if counts["lease_history_git_reads"] != wantReads || counts["lease_history_cache_hits"] != wantHits {
			t.Fatalf("unexpected cache work: %v", counts)
		}
		return events
	}
	want := read(2, 0)
	if got := read(0, 1); !reflect.DeepEqual(got, want) {
		t.Fatal("receipt changed event identities, attribution or order")
	}
	var saved leaseHistoryReceipt
	key := refs.historyKey(ref)
	if err := refs.archive.Status(key, &saved); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*leaseHistoryReceipt)
	}{
		{"corrupt-object", func(r *leaseHistoryReceipt) { r.Commits[0] = []byte("tampered") }},
		{"truncated-chain", func(r *leaseHistoryReceipt) { r.Commits = r.Commits[:1] }},
		{"excess-chain", func(r *leaseHistoryReceipt) { r.Commits = append(r.Commits, []byte("extra")) }},
		{"wrong-tip", func(r *leaseHistoryReceipt) { r.Tip = first }},
		{"old-version", func(r *leaseHistoryReceipt) { r.Version = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := saved
			bad.Commits = append([][]byte(nil), saved.Commits...)
			tc.mutate(&bad)
			if err := refs.archive.SaveStatus(key, bad); err != nil {
				t.Fatal(err)
			}
			if got := read(2, 0); !reflect.DeepEqual(got, want) {
				t.Fatal("fallback changed history")
			}
		})
	}
	// Missing and independently damaged status checksums both force Git reads.
	for _, query := range []string{
		"DELETE FROM history_archive_status",
		"UPDATE history_archive_status SET checksum='0000000000000000000000000000000000000000000000000000000000000000'",
	} {
		if _, err := refs.archive.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
		read(2, 0)
	}
	event.Sequence = 3
	tip = leaseHistoryCommit(t, transport, event, tip)
	want = read(3, 0)
	if got := read(0, 1); !reflect.DeepEqual(got, want) {
		t.Fatal("moved tip did not replace receipt")
	}
	// A cached chain cannot be transplanted to a different ref or lineage.
	if got, err := refs.cachedEvents(context.Background(), transport, refs.ref("task", "other"), tip); err != nil || len(got) != 3 {
		t.Fatalf("fallback must return actual scope for Scopes to reject: %v", err)
	}
	var wrong leaseHistoryReceipt
	if err := refs.archive.Status(refs.historyKey(refs.ref("task", "other")), &wrong); err == nil {
		t.Fatal("wrong-scope history cached")
	}
}

func TestLeaseHistoryCacheBoundsAndMalformedEvents(t *testing.T) {
	t.Parallel()
	refs, transport, event := leaseHistoryFixture(t)
	var tip string
	for i := 1; i <= leaseHistory+2; i++ {
		event.Sequence = i
		if i > 1 {
			event.Action = "renew"
		}
		var parents []string
		if tip != "" {
			parents = []string{tip}
		}
		tip = leaseHistoryCommit(t, transport, event, parents...)
	}
	ref := refs.ref(event.ScopeKind, event.ScopeID)
	for i := 0; i < 2; i++ {
		events, err := refs.cachedEvents(context.Background(), transport, ref, tip)
		if err != nil || len(events) != leaseHistory || events[leaseHistory-1].Sequence != 3 {
			t.Fatalf("bounded history: %d %v", len(events), err)
		}
	}
	// Valid commit hashes do not excuse invalid events, trees, parent count,
	// scope changes or truncated raw receipts.
	for _, tc := range []struct{ name, message string }{
		{"unknown-field", `{"unexpected":true}`},
		{"trailing-data", `{} {}`},
		{"invalid-event", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree, _ := transport.writeTree(context.Background(), nil, 40)
			oid, err := transport.writeCommit(context.Background(), tree, nil, tc.message)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := refs.cachedEvents(context.Background(), transport, ref, oid); err == nil {
				t.Fatal("invalid lease history accepted")
			}
		})
	}
	event.ScopeID = "other"
	changed := leaseHistoryCommit(t, transport, event, tip)
	if _, err := refs.cachedEvents(context.Background(), transport, ref, changed); err == nil || !strings.Contains(err.Error(), "changes scope") {
		t.Fatalf("scope change accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := refs.cachedEvents(ctx, transport, ref, tip); err == nil {
		t.Fatal("cancelled history read accepted")
	}
	// Histories with valid but large events still work, without oversized cache
	// writes or a second base64 representation of a maximum-sized chain.
	event.ScopeID = "build"
	event.Holder = "holder"
	large := tip
	for i := 0; i < leaseHistory; i++ {
		event.Sequence++
		tree, err := transport.writeTree(context.Background(), nil, 40)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(event)
		large, err = transport.writeCommit(context.Background(), tree, []string{large}, strings.Repeat("x", 3000)+"\n"+string(body)+"\n")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := refs.cachedEvents(context.Background(), transport, ref, large); err != nil {
		t.Fatal(err)
	}
	var receipt leaseHistoryReceipt
	if err := refs.archive.Status(refs.historyKey(ref), &receipt); err == nil && receipt.Tip == large {
		t.Fatal("oversized history cached")
	}
	if _, err := decodeTransportCommit(tip, make([]byte, maxTransportCommitBytes+1)); err == nil {
		t.Fatal("oversized raw object accepted")
	}
}
