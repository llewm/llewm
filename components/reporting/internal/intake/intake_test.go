package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These protocol fixtures use http.Client's real request/redirect handling with
// an in-memory transport. They do not require sandbox listener permissions and
// do not claim TLS or live-instance qualification.
var fixtureSequence atomic.Int64
var fixtureHandlers sync.Map

type fixtureServer struct{ URL, host string }
type fixtureTransport struct{}

func (fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	handler, ok := fixtureHandlers.Load(request.URL.Host)
	if !ok {
		return nil, fmt.Errorf("unknown fixture host")
	}
	recorder := httptest.NewRecorder()
	handler.(http.Handler).ServeHTTP(recorder, request)
	response := recorder.Result()
	response.Request = request
	return response, nil
}
func newFixtureServer(handler http.Handler) *fixtureServer {
	host := fmt.Sprintf("forge-%d.invalid", fixtureSequence.Add(1))
	fixtureHandlers.Store(host, handler)
	return &fixtureServer{URL: "https://" + host, host: host}
}
func (s *fixtureServer) Client() *http.Client { return &http.Client{Transport: fixtureTransport{}} }
func (s *fixtureServer) Close()               { fixtureHandlers.Delete(s.host) }

func reportFixture(t *testing.T, dir, expected string) string {
	t.Helper()
	r := Report{SchemaVersion: 1, ReportID: "sha256:" + strings.Repeat("a", 64), Title: "Bootstrap failed", Description: "token=fixture-report-secret", Observed: "Missing packet", Expected: expected, Reproduction: "Fresh session", Environment: "Linux", Severity: "high", SupplementAuthority: "caller_asserted", Missing: []string{}, Disclaimer: "Draft"}
	r.Source.ProjectID, r.Source.Ref, r.Source.Creator, r.Source.Authority = "project-one", "work_item:bug-one", "agent", "model_suggested"
	r.Source.RecordedAt = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "report.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeConfig(t *testing.T, url, dir string) Config {
	t.Helper()
	c := Config{RepositoryURL: url + "/owner/repo", AllowedProjectIDs: []string{"project-one"}, AutomaticSubmission: true, TokenEnv: "LLEWM_TEST_TOKEN", Outbox: "outbox"}
	body, _ := json.Marshal(c)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

type forgeState struct {
	mu         sync.Mutex
	private    bool
	failStatus int
	ambiguous  bool
	posts      int
	issues     []issue
	comments   map[int][]string
}

func (s *forgeState) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("Authorization") != "token fixture-credential-secret" {
		w.WriteHeader(401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if s.failStatus != 0 {
		w.WriteHeader(s.failStatus)
		fmt.Fprint(w, "fixture-credential-secret")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/owner/repo")
	if path == "" {
		fmt.Fprintf(w, `{"private":%t,"has_issues":true,"archived":false}`, s.private)
		return
	}
	if path == "/issues" && r.Method == http.MethodGet {
		// Simulate an instance limiting each page to one result, regardless of
		// the client's requested 50. X-Total-Count must prevent an early stop.
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("X-Total-Count", strconv.Itoa(len(s.issues)))
		result := []issue{}
		if page > 0 && page <= len(s.issues) {
			result = append(result, s.issues[page-1])
		}
		json.NewEncoder(w).Encode(result)
		return
	}
	if r.Method == http.MethodPost && path == "/issues" {
		var payload struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		created := issue{Index: len(s.issues) + 1, Body: payload.Body}
		s.issues = append(s.issues, created)
		s.posts++
		if s.ambiguous {
			s.ambiguous = false
			fmt.Fprint(w, "lost-response")
			return
		}
		json.NewEncoder(w).Encode(created)
		return
	}
	var index int
	if _, err := fmt.Sscanf(path, "/issues/%d/comments", &index); err == nil && strings.HasSuffix(path, "/comments") {
		if r.Method == http.MethodGet {
			result := []map[string]string{}
			for _, body := range s.comments[index] {
				result = append(result, map[string]string{"body": body})
			}
			json.NewEncoder(w).Encode(result)
			return
		}
		var payload struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		s.comments[index] = append(s.comments[index], payload.Body)
		s.posts++
		if s.ambiguous {
			s.ambiguous = false
			fmt.Fprint(w, "lost-response")
			return
		}
		fmt.Fprint(w, `{"id":7}`)
		return
	}
	w.WriteHeader(404)
}

func TestPrivateDeliveryQueueRecoveryAndUpdatedEvidence(t *testing.T) {
	t.Setenv("LLEWM_TEST_TOKEN", "fixture-credential-secret")
	dir := t.TempDir()
	state := &forgeState{private: true, ambiguous: true, comments: map[int][]string{}, issues: []issue{{Index: 1, Body: "unrelated"}}}
	server := newFixtureServer(http.HandlerFunc(state.handler))
	defer server.Close()
	a := Adapter{Config: writeConfig(t, server.URL, dir), Client: server.Client()}
	path := reportFixture(t, dir, "Load packet")
	entry, err := a.Queue(path)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := os.ReadFile(filepath.Join(a.Config.Outbox, entry.ID+".json"))
	if strings.Contains(string(stored), "fixture-report-secret") || strings.Contains(string(stored), "fixture-credential-secret") {
		t.Fatal("secret persisted")
	}
	repeated, err := a.Queue(path)
	if err != nil || repeated.ID != entry.ID {
		t.Fatalf("queue is not idempotent: %#v %v", repeated, err)
	}
	if _, err := a.Deliver(context.Background()); err == nil {
		t.Fatal("ambiguous response incorrectly acknowledged")
	}
	status, err := a.Status()
	if err != nil || len(status) != 1 || status[0].Status != "pending" {
		t.Fatalf("pending report lost: %v %v", status, err)
	}
	receipts, err := a.Deliver(context.Background())
	if err != nil || len(receipts) != 1 || receipts[0].Receipt.Outcome != "recovered" || receipts[0].Receipt.Issue != 2 {
		t.Fatalf("recovery failed: %v %v", receipts, err)
	}
	updated, err := a.Queue(reportFixture(t, dir, "Load packet exactly once"))
	if err != nil || updated.ID == entry.ID {
		t.Fatalf("updated evidence not distinct: %#v %v", updated, err)
	}
	state.mu.Lock()
	state.ambiguous = true
	state.mu.Unlock()
	if _, err := a.Deliver(context.Background()); err == nil {
		t.Fatal("ambiguous comment acknowledged")
	}
	receipts, err = a.Deliver(context.Background())
	if err != nil || len(receipts) != 1 || receipts[0].Receipt.Outcome != "recovered" {
		t.Fatalf("comment recovery failed: %v %v", receipts, err)
	}
	state.mu.Lock()
	if state.posts != 2 || len(state.issues) != 2 || len(state.comments[2]) != 1 {
		t.Fatalf("retry duplicated delivery: %#v", state)
	}
	state.mu.Unlock()
	if receipts, err := a.Deliver(context.Background()); err != nil || len(receipts) != 0 {
		t.Fatalf("delivered report resubmitted: %v %v", receipts, err)
	}
}

func TestDeliveryFailsClosedAndRetainsPending(t *testing.T) {
	t.Setenv("LLEWM_TEST_TOKEN", "fixture-credential-secret")
	dir := t.TempDir()
	state := &forgeState{private: false, comments: map[int][]string{}}
	server := newFixtureServer(http.HandlerFunc(state.handler))
	defer server.Close()
	a := Adapter{Config: writeConfig(t, server.URL, dir), Client: server.Client()}
	if _, err := a.Queue(reportFixture(t, dir, "Load packet")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deliver(context.Background()); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("public target accepted: %v", err)
	}
	state.mu.Lock()
	state.private, state.failStatus = true, 503
	state.mu.Unlock()
	if _, err := a.Deliver(context.Background()); err == nil || strings.Contains(err.Error(), "fixture-credential-secret") {
		t.Fatalf("failed response unsafe: %v", err)
	}
	status, err := a.Status()
	if err != nil || status[0].Status != "pending" {
		t.Fatalf("failed report lost: %v %v", status, err)
	}
	state.mu.Lock()
	state.failStatus = 0
	state.mu.Unlock()
	a.Config.AutomaticSubmission = false
	if _, err := a.Deliver(context.Background()); err == nil {
		t.Fatal("missing opt-in accepted")
	}
	a.Config.AutomaticSubmission = true
	a.Config.AllowedProjectIDs = []string{"other-project"}
	if _, err := a.Deliver(context.Background()); err == nil {
		t.Fatal("removed source opt-in ignored")
	}
	a.Config.AllowedProjectIDs = []string{"project-one"}
	a.Config.RepositoryURL = server.URL + "/owner/other"
	if _, err := a.Deliver(context.Background()); err == nil {
		t.Fatal("pending report silently retargeted")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.posts != 0 {
		t.Fatal("fail-closed cases posted data")
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	t.Setenv("LLEWM_TEST_TOKEN", "fixture-credential-secret")
	called := false
	destination := newFixtureServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer destination.Close()
	server := newFixtureServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer server.Close()
	dir := t.TempDir()
	a := Adapter{Config: writeConfig(t, server.URL, dir), Client: server.Client()}
	if _, err := a.Queue(reportFixture(t, dir, "Expected")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deliver(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
	if called {
		t.Fatal("followed credential-bearing redirect")
	}
}

func TestQueueUnknownFieldsAndProjectScope(t *testing.T) {
	dir := t.TempDir()
	a := Adapter{Config: Config{RepositoryURL: "https://forge.example/owner/repo", AllowedProjectIDs: []string{"different"}, Outbox: filepath.Join(dir, "outbox")}}
	path := reportFixture(t, dir, "Expected")
	if _, err := a.Queue(path); err == nil {
		t.Fatal("unlisted project queued")
	}
	a.Config.AllowedProjectIDs = []string{"project-one"}
	body, _ := os.ReadFile(path)
	var value map[string]any
	json.Unmarshal(body, &value)
	value["transcript"] = "private transcript"
	body, _ = json.Marshal(value)
	os.WriteFile(path, body, 0o600)
	if _, err := a.Queue(path); err == nil {
		t.Fatal("unknown data silently persisted")
	}
	if _, err := os.Stat(a.Config.Outbox); !os.IsNotExist(err) {
		t.Fatal("invalid report created outbox")
	}
}

func TestRejectDuplicateJSONKeys(t *testing.T) {
	for _, body := range []string{`{"schema_version":1,"schema_version":2}`, `{"source":{"project_id":"one","project_id":"two"}}`, `{"nested":[{"token":"one","token":"two"}]}`} {
		if err := strictJSON([]byte(body), new(any)); err == nil {
			t.Fatalf("ambiguous JSON accepted: %s", body)
		}
	}
}

func TestOutboxLockAndTamperDetection(t *testing.T) {
	dir := t.TempDir()
	a := Adapter{Config: Config{RepositoryURL: "https://forge.example/owner/repo", AllowedProjectIDs: []string{"project-one"}, Outbox: filepath.Join(dir, "outbox")}}
	path := reportFixture(t, dir, "Expected")
	entry, err := a.Queue(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := a.lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Queue(path); err == nil {
		t.Fatal("concurrent queue ignored the lock")
	}
	unlock()
	entry.Report.Description = "tampered content"
	body, _ := json.Marshal(entry)
	if err := os.WriteFile(filepath.Join(a.Config.Outbox, entry.ID+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Status(); err == nil {
		t.Fatal("modified queued content retained original identity")
	}
}

func TestStatusSnapshotContainsOnlyVersionedDeliveryMetadata(t *testing.T) {
	dir := t.TempDir()
	a := Adapter{Config: writeConfig(t, "https://forge.invalid", dir), Now: func() time.Time { return time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC) }}
	if _, err := a.Queue(reportFixture(t, dir, "Expected behavior")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := a.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != 1 || snapshot.Authority != "machine_observed" || !snapshot.ObservedAt.Equal(a.Now()) || len(snapshot.Reports) != 1 {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	r := snapshot.Reports[0]
	if r.ProjectID != "project-one" || r.ReportID == "" || r.QueuedAt.IsZero() || r.Status != "pending" {
		t.Fatalf("summary: %+v", r)
	}
	empty, err := a.Snapshot("other-project")
	if err != nil || len(empty.Reports) != 0 {
		t.Fatalf("project filter: %+v %v", empty, err)
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"fixture-report-secret", "Description", "description", "Expected behavior", "token_env", "outbox"} {
		if strings.Contains(string(body), unwanted) {
			t.Fatalf("metadata exposed %q", unwanted)
		}
	}
}
