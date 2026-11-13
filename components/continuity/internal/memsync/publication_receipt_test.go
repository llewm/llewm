package memsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func publicationCheckpoint(t *testing.T, m machine, task string) string {
	t.Helper()
	r, err := (checkpoint.Service{DB: m.db}).CreateWithActions(context.Background(), checkpoint.Checkpoint{
		ProjectID: m.project, TaskID: task, Task: "Publication verification", Status: "in_progress", Summary: "Saved work", NextAction: "Review result",
	})
	if err != nil {
		t.Fatal(err)
	}
	return "checkpoint:" + r.Checkpoint.ID
}

// Serial: it changes the process-wide buildinfo.Commit, which binds every
// concurrent test's observation route.
func TestPublicationReceiptZeroExportAndExactRecordCheck(t *testing.T) {
	oldCommit := buildinfo.Commit
	buildinfo.Commit = strings.Repeat("a", 40)
	t.Cleanup(func() { buildinfo.Commit = oldCommit })
	ctx := context.Background()
	m := newMachine(t, "publication", bareRemote(t))
	ref := publicationCheckpoint(t, m, "first")
	if _, err := (worknarrative.Service{DB: m.db}).Append(ctx, worknarrative.Record{ID: "published-story", ProjectID: m.project, Title: "Report", Creator: "test", Trigger: "manual", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Work done", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	first := m.sync(t, OutcomeSynced)
	if first.Publication == nil || first.Publication.State != "snapshot_published" || first.Publication.PeerReceipt != "unconfirmed" {
		t.Fatalf("missing receipt: %+v", first)
	}
	for i := 0; i < 2; i++ {
		got := m.sync(t, OutcomeSynced)
		if got.ExportedRecords != 0 || got.Publication == nil || got.Publication.State != "snapshot_published" {
			t.Fatalf("zero export lost confirmation: %+v", got)
		}
		if i == 1 && !got.Unchanged {
			t.Fatal("receipt writes invalidated unchanged proof reuse")
		}
	}
	late := publicationCheckpoint(t, m, "after-snapshot")
	var before int
	if err := m.db.QueryRow(`SELECT count(*) FROM sync_attempts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	check, err := m.syncer.CheckPublication(ctx, []string{ref, "narrative:published-story", late}, "")
	if err != nil || check.RemoteHead == "" || check.ObservedAt == "" || check.PeerReceipt != "unconfirmed" {
		t.Fatalf("check: %+v %v", check, err)
	}
	for i, want := range []string{"published", "published", "not_in_observed_remote"} {
		if check.Records[i].State != want {
			t.Fatalf("record%d: %+v", i, check)
		}
	}
	hash := check.Records[0].EnvelopeHash[0]
	exact, err := m.syncer.CheckPublication(ctx, []string{ref}, hash)
	if err != nil || exact.Records[0].State != "published" {
		t.Fatalf("exact: %+v %v", exact, err)
	}
	missing, err := m.syncer.CheckPublication(ctx, []string{ref}, strings.Repeat("0", 64))
	if err != nil || missing.Records[0].State != "not_in_observed_remote" {
		t.Fatalf("wrong bytes accepted: %+v %v", missing, err)
	}
	var after int
	if err := m.db.QueryRow(`SELECT count(*) FROM sync_attempts`).Scan(&after); err != nil || before != after {
		t.Fatalf("read check recorded a sync: %d %d %v", before, after, err)
	}
	if last, err := m.syncer.PublicationStatus(ctx); err != nil || last == nil || last.State != "snapshot_published" {
		t.Fatalf("last observation: %+v %v", last, err)
	}
	other := m.syncer
	other.Remote += "-other"
	if last, err := other.PublicationStatus(ctx); err != nil || last != nil {
		t.Fatalf("another endpoint reused receipt: %+v %v", last, err)
	}
	for _, commit := range []string{"unknown", strings.Repeat("b", 40)} {
		buildinfo.Commit = commit
		if last, err := m.syncer.PublicationStatus(ctx); err != nil || last != nil {
			t.Fatalf("unknown/other build reused receipt: %+v %v", last, err)
		}
	}
	buildinfo.Commit = strings.Repeat("a", 40)
	if _, err := m.db.Exec(`UPDATE history_archive_status SET checksum=? WHERE project_id=? AND consumer=?`, strings.Repeat("0", 64), m.project, recordsync.Hash(m.syncer.publicationReceiptKey(testLineage))); err != nil {
		t.Fatal(err)
	}
	if last, err := m.syncer.PublicationStatus(ctx); err == nil || last != nil {
		t.Fatalf("damaged receipt trusted: %+v %v", last, err)
	}
}

func TestPublicationReceiptDeferredOfflineAndDeletedRemote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := bareRemote(t)
	m := newMachine(t, "delivery-failures", remote)
	ref := publicationCheckpoint(t, m, "pending")
	deferred, err := m.syncer.Session(ctx, 10*time.Second)
	if err != nil || deferred.Outcome != OutcomeDeferred || deferred.Publication == nil || deferred.Publication.State != "unconfirmed" {
		t.Fatalf("deferred delivery claimed: %+v %v", deferred, err)
	}
	if check, err := m.syncer.CheckPublication(ctx, []string{ref}, ""); err != nil || check.Records[0].State != "not_in_observed_remote" {
		t.Fatalf("local commit counted as remote: %+v %v", check, err)
	}
	m.sync(t, OutcomeSynced)
	if _, err := (git{Dir: remote}).run(ctx, "update-ref", "-d", "refs/heads/"+deferred.Publication.Branch); err != nil {
		t.Fatal(err)
	}
	check, err := m.syncer.CheckPublication(ctx, []string{ref}, "")
	if err != nil || check.RemoteHead != "" || check.Records[0].State != "not_in_observed_remote" {
		t.Fatalf("deleted remote branch reused stale tracking ref: %+v %v", check, err)
	}
	m.sync(t, OutcomeSynced)
	if err := os.Rename(remote, remote+"-offline"); err != nil {
		t.Fatal(err)
	}
	publicationCheckpoint(t, m, "new-offline")
	offline := m.sync(t, OutcomeOffline)
	if offline.Publication == nil || offline.Publication.State != "unconfirmed" || offline.Publication.RemoteHead != "" {
		t.Fatalf("offline delivery claimed: %+v", offline)
	}
	check, err = m.syncer.CheckPublication(ctx, []string{ref}, "")
	if err == nil || check.Records[0].State != "unconfirmed" || check.ObservedAt != "" {
		t.Fatalf("failed network reused cached success: %+v %v", check, err)
	}
}

func TestPublicationSnapshotExcludesWriteDuringPush(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var delay atomic.Int64
	upstream := delayedHTTPRemote(t, bareRemote(t), &delay)
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	path := target.Path
	target.Path = ""
	proxy := httputil.NewSingleHostReverseProxy(target)
	var inject atomic.Bool
	var refuse atomic.Bool
	var once sync.Once
	var m machine
	var late string
	var saveErr error
	injected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() && r.URL.Query().Get("service") == "git-receive-pack" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if inject.Load() && r.Method == "GET" && r.URL.Query().Get("service") == "git-receive-pack" {
			once.Do(func() {
				defer close(injected)
				saved, err := (checkpoint.Service{DB: m.db}).CreateWithActions(ctx, checkpoint.Checkpoint{ProjectID: m.project, TaskID: "during-push", Task: "Later work", Status: "in_progress", Summary: "Created after capture", NextAction: "Sync later"})
				saveErr, late = err, "checkpoint:"+saved.Checkpoint.ID
			})
		}
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	m = newMachine(t, "concurrent-write", server.URL+path)
	first := publicationCheckpoint(t, m, "captured")
	inject.Store(true)
	r := m.sync(t, OutcomeSynced)
	select {
	case <-injected:
	case <-time.After(5 * time.Second):
		t.Fatal("push did not reach the injection boundary")
	}
	if saveErr != nil || late == "" || r.Publication == nil || r.Publication.State != "snapshot_published" {
		t.Fatalf("push boundary: %+v %s %v", r, late, saveErr)
	}
	check, err := m.syncer.CheckPublication(ctx, []string{first, late}, "")
	if err != nil || check.Records[0].State != "published" || check.Records[1].State != "not_in_observed_remote" {
		t.Fatalf("receipt overclaimed concurrent write: %+v %v", check, err)
	}
	m.sync(t, OutcomeSynced)
	check, err = m.syncer.CheckPublication(ctx, []string{late}, "")
	if err != nil || check.Records[0].State != "published" {
		t.Fatalf("later write not delivered next cycle: %+v %v", check, err)
	}
	refused := publicationCheckpoint(t, m, "refused-push")
	refuse.Store(true)
	failed := m.sync(t, OutcomeOffline)
	if failed.Publication == nil || failed.Publication.State != "unconfirmed" {
		t.Fatalf("rejected push confirmed: %+v", failed)
	}
	check, err = m.syncer.CheckPublication(ctx, []string{refused}, "")
	if err != nil || check.Records[0].State != "not_in_observed_remote" {
		t.Fatalf("rejected push present remotely: %+v %v", check, err)
	}
}

func TestPublicationCheckRejectsInvalidSelectorsBeforeGit(t *testing.T) {
	t.Parallel()
	for _, refs := range [][]string{nil, {"decision:example"}, {"checkpoint:"}, {"narrative:x\nsecret"}, {"checkpoint:x", "checkpoint:x"}} {
		if _, err := (Syncer{}).CheckPublication(context.Background(), refs, ""); err == nil {
			t.Fatalf("accepted %q", refs)
		}
	}
}

func TestPublicationCheckRejectsDamagedRemoteInsteadOfCachedSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := bareRemote(t)
	m := newMachine(t, "damaged-remote", remote)
	ref := publicationCheckpoint(t, m, "valid-before-damage")
	good := m.sync(t, OutcomeSynced)
	transport := &objectTransport{repo: git{Dir: remote, Identity: []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}}}
	leaf := objectBlob(t, transport, strings.Repeat("0", 64)+".block", "damaged block bytes", 40)
	tree := transportBlockTree(t, transport, "v5", testLineage, []blockTreeEntry{leaf}, 40)
	bad, err := transport.writeCommit(ctx, tree, []string{good.Publication.RemoteHead}, "Damaged fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.repo.run(ctx, "update-ref", "refs/heads/"+good.Publication.Branch, bad); err != nil {
		t.Fatal(err)
	}
	got, err := m.syncer.CheckPublication(ctx, []string{ref}, "")
	if err == nil || got.Records[0].State != "unconfirmed" || got.ObservedAt != "" {
		t.Fatalf("damaged remote reused success: %+v %v", got, err)
	}
}
