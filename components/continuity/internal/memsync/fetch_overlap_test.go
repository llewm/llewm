package memsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchOverlapsCaptureAndOfflinePublicationStillCommits(t *testing.T) {
	for _, mode := range []string{"offline", "local_failure"} {
		t.Run(mode, func(t *testing.T) { testFetchOverlapBoundaries(t, mode == "local_failure") })
	}
}

func testFetchOverlapBoundaries(t *testing.T, failLocal bool) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	var delay atomic.Int64
	upstream := delayedHTTPRemote(t, bareRemote(t), &delay)
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	path := target.Path
	target.Path = ""
	proxy := httputil.NewSingleHostReverseProxy(target)
	var block atomic.Bool
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce, finishedOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if block.Load() && r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/info/refs") {
			defer finishedOnce.Do(func() { close(finished) })
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer unblock()
	m := newMachine(t, "overlap", server.URL+path)
	m.syncer.NoPrompt = true
	m.decide(t, "existing", "Existing history")
	warmUnchanged(t, m)
	archive := archiveMachine(t, &m)
	m.syncer.Archive = &archive
	repo := git{Dir: m.syncer.Clone}
	old, err := repo.run(context.Background(), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	m.decide(t, "new", "Local publication survives failed fetch")
	writer, err := m.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.Exec(`UPDATE projects SET name=name WHERE id=?`, m.project); err != nil {
		t.Fatal(err)
	}
	block.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := m.syncer.Sync(ctx); done <- outcome{result, err} }()
	joined := false
	defer func() {
		cancel()
		writer.Rollback()
		unblock()
		if !joined {
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				t.Error("sync worker did not finish")
			}
		}
	}()
	// Capture cannot reserve its writer yet. Fetch must nevertheless start.
	select {
	case <-entered:
	case result := <-done:
		joined = true
		t.Fatalf("sync ended before overlapping fetch: %+v", result)
	case <-time.After(3 * time.Second):
		t.Fatal("fetch waited for capture's writer")
	}
	if failLocal {
		// Fetch is definitely in flight. Fail the new archive row, preserving
		// the existing observation/capture transaction boundaries and error.
		if _, err := writer.Exec(`CREATE TRIGGER fixture_capture_failure BEFORE INSERT ON history_archive_envelopes BEGIN SELECT RAISE(ABORT,'fixture local capture failure'); END`); err != nil {
			t.Fatal(err)
		}
		if err := writer.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-done:
			joined = true
			if result.err == nil || !strings.Contains(result.err.Error(), "fixture local capture failure") || result.result.Outcome != OutcomeError || ctx.Err() != nil {
				t.Fatalf("local failure lost or caller canceled: %+v %v", result, ctx.Err())
			}
		case <-ctx.Done():
			t.Fatal("local failure left fetch waiting")
		}
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatal("local failure did not cancel HTTP fetch")
		}
		if _, err := m.db.Exec(`DROP TRIGGER fixture_capture_failure`); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := writer.Rollback(); err != nil {
			t.Fatal(err)
		}
		// Now local capture/publication must finish while the remote is still held.
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			head, err := repo.run(ctx, "rev-parse", "HEAD")
			if err == nil && head != old {
				break
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("local publication waited for fetch")
			}
		}
		select {
		case result := <-done:
			joined = true
			t.Fatalf("fetch finished while held: %+v", result)
		default:
		}
		unblock()
		select {
		case result := <-done:
			joined = true
			if result.err != nil || result.result.Outcome != OutcomeOffline || result.result.ExportedRecords != 1 || result.result.UnpushedCommits != 1 {
				t.Fatalf("offline local boundary changed: %+v", result)
			}
		case <-ctx.Done():
			t.Fatal("sync did not finish after releasing fetch")
		}
	}
	for _, name := range []string{"FETCH_HEAD.lock", "shallow.lock", "index.lock"} {
		if _, err := os.Lstat(filepath.Join(m.syncer.Clone, name)); !os.IsNotExist(err) {
			t.Fatalf("fetch left %s: %v", name, err)
		}
	}
	unblock()
	block.Store(false)
	m.sync(t, OutcomeSynced)
	other := newMachine(t, "receiver", upstream)
	other.sync(t, OutcomeSynced)
	if got := other.decisions(t); got != "existing,new" {
		t.Fatalf("local commit lost after retry: %s", got)
	}
}
