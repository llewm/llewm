package memsync

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// A real local smart-HTTP remote with controlled advertisement latency. Git is
// started through the same sanitized entry point as other component fixtures.
func delayedHTTPRemote(t testing.TB, remote string, delay *atomic.Int64) string {
	t.Helper()
	if _, err := (git{Dir: remote}).run(context.Background(), "config", "http.receivepack", "true"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/info/refs") {
			if duration := time.Duration(delay.Load()); duration > 0 {
				timer := time.NewTimer(duration)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					return
				}
			}
		}
		cmd := gitenv.Command(r.Context(), "-c", "core.hooksPath="+os.DevNull, "-c", "gc.auto=0", "http-backend")
		cmd.Env = append(gitenv.Environ(), "GIT_PROJECT_ROOT="+filepath.Dir(remote), "GIT_HTTP_EXPORT_ALL=1", "PATH_INFO="+r.URL.Path, "REQUEST_METHOD="+r.Method, "QUERY_STRING="+r.URL.RawQuery, "CONTENT_TYPE="+r.Header.Get("Content-Type"), "CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10))
		cmd.Stdin = r.Body
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr, cmd.WaitDelay = &out, &stderr, 3*time.Second
		if err := cmd.Run(); err != nil {
			http.Error(w, stderr.String(), http.StatusInternalServerError)
			return
		}
		reader := bufio.NewReader(&out)
		headers, err := textproto.NewReader(reader).ReadMIMEHeader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		for name, values := range headers {
			if name == "Status" {
				status, _ = strconv.Atoi(strings.Fields(values[0])[0])
				continue
			}
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(status)
		_, _ = io.Copy(w, reader)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/" + filepath.Base(remote)
}

func BenchmarkRemoteFetchOverlap(b *testing.B) {
	for _, size := range []int{500, 5669} {
		b.Run(fmt.Sprintf("records_%d", size), func(b *testing.B) {
			var delay atomic.Int64
			m := newMachine(b, "fetch-overlap", delayedHTTPRemote(b, bareRemote(b), &delay))
			for i := 0; i < size; i++ {
				m.decide(b, fmt.Sprintf("record-%d", i), "Synthetic full validation")
			}
			for range 2 {
				m.sync(b, OutcomeSynced)
			}
			delay.Store(int64(1500 * time.Millisecond))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				fresh, err := syncwork.WithFreshValidation(context.Background())
				if err != nil {
					b.Fatal(err)
				}
				ctx, work := syncwork.WithWorkMetrics(fresh)
				result, err := m.syncer.Sync(ctx)
				if err != nil || result.Outcome != OutcomeSynced || result.Unchanged || result.ExportedRecords+result.ImportedRecords+result.Collisions+result.Forks+result.DeferredRecords != 0 {
					b.Fatalf("full sync: %+v %v", result, err)
				}
				counts := work.Counts()
				if counts["capture_full_exports"] != 1 || counts["full_semantic_records"] < int64(size) {
					b.Fatalf("missing full work: %v", counts)
				}
				b.ReportMetric(float64(counts["full_semantic_records"]), "semantic_records/op")
			}
		})
	}
}
