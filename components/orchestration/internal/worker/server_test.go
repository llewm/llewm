package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectClient(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := mcp.NewClient(&mcp.Implementation{Name: "worker-test", Version: "1"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); ss.Close() })
	return c
}

func callTool(t *testing.T, c *mcp.ClientSession, name string, args any, fail bool) *mcp.CallToolResult {
	t.Helper()
	r, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError != fail {
		t.Fatalf("%s error=%v content=%+v", name, r.IsError, r.Content)
	}
	return r
}

func decodeResult[T any](t *testing.T, r *mcp.CallToolResult) T {
	t.Helper()
	var out T
	b, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMCPWorkerConsumesMultipleJobsAndReportsWithoutExecution(t *testing.T) {
	ctx := context.Background()
	store, err := coordination.Open(filepath.Join(t.TempDir(), "private", "team.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	operator, err := store.Enroll(ctx, "operator", []string{"plan", "accept", "assign", "review", "work"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.Enroll(ctx, "worker", []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	job := func(priority int) *coordination.JobSpec {
		return &coordination.JobSpec{OS: runtime.GOOS, Arch: runtime.GOARCH, Capabilities: []string{"native-tests"}, Priority: priority, Repository: "https://github.com/llewm/llewm", BaseRevision: "commit:abc", AllowedChanges: []string{"temporary test fixtures"}, ApprovalRequired: []string{"source publication"}}
	}
	d := &coordination.Definition{Title: "Approved native jobs", SourceRef: "continuity:project/plan:test@1", Items: []coordination.Item{{ID: "first", Title: "First", AcceptanceCriteria: "Evidence", Job: job(10)}, {ID: "second", Title: "Second", AcceptanceCriteria: "Evidence", Job: job(20)}, {ID: "third", Title: "Third", AcceptanceCriteria: "Evidence", Job: job(30)}}}
	r, err := store.Execute(ctx, operator, coordination.Command{SchemaVersion: 1, PlanID: "plan", RequestID: "propose", Action: "propose", Definition: d})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Execute(ctx, operator, coordination.Command{SchemaVersion: 1, PlanID: "plan", RequestID: "accept", Action: "accept-plan", Revision: 1, BaseVersion: r.Version})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(transport.Handler(store))
	defer httpServer.Close()
	s, err := NewServer(Config{Endpoint: httpServer.URL, Token: token, PlanIDs: []string{"plan"}, Capabilities: []string{"native-tests"}, Machine: "test-pc", Agent: "existing-claude"})
	if err != nil {
		t.Fatal(err)
	}
	c := connectClient(t, s)
	tools, err := c.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 11 {
		t.Fatalf("unexpected exposed tools: %d", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "exec") || strings.Contains(tool.Name, "launch") {
			t.Fatal("execution exposed")
		}
	}
	info := decodeResult[Info](t, callTool(t, c, "work_info", InfoInput{}, false))
	if info.OS != runtime.GOOS || info.Arch != runtime.GOARCH || len(info.PlanIDs) != 1 || info.PlanIDs[0] != "plan" {
		t.Fatalf("wrong adapter identity: %+v", info)
	}
	queue := decodeResult[coordination.Queue](t, callTool(t, c, "work_queue", map[string]any{}, false))
	if len(queue.Jobs) != 3 || queue.Jobs[0].Item.ID != "first" {
		t.Fatalf("queue: %+v", queue)
	}
	claim := decodeResult[coordination.Receipt](t, callTool(t, c, "work_claim", ClaimInput{PlanID: "plan", RequestID: "claim-first", BaseVersion: queue.Jobs[0].Version, ItemID: "first"}, false))
	callTool(t, c, "work_renew", AttemptInput{PlanID: "plan", RequestID: "renew-first", AttemptID: claim.AttemptID}, false)
	callTool(t, c, "work_checkpoint", ReportInput{PlanID: "plan", RequestID: "progress-first", AttemptID: claim.AttemptID, Context: "Checking the reviewed native fixture"}, false)
	sub := decodeResult[coordination.Receipt](t, callTool(t, c, "work_submit", ReportInput{PlanID: "plan", RequestID: "result-first", AttemptID: claim.AttemptID, Context: "Native fixture verified", ArtifactRefs: []string{"artifact:fixture"}, VerificationRefs: []string{"test:native"}}, false))
	if sub.Status != "pending_review" {
		t.Fatal("result was self-approved")
	}
	queue = decodeResult[coordination.Queue](t, callTool(t, c, "work_queue", map[string]any{}, false))
	if len(queue.Jobs) != 2 || queue.Jobs[0].Item.ID != "second" {
		t.Fatalf("waiting review blocked independent work: %+v", queue)
	}
	second := decodeResult[coordination.Receipt](t, callTool(t, c, "work_claim", ClaimInput{PlanID: "plan", RequestID: "claim-second", BaseVersion: queue.Jobs[0].Version, ItemID: "second"}, false))
	callTool(t, c, "work_pause", PauseInput{PlanID: "plan", RequestID: "pause-second", AttemptID: second.AttemptID, Reason: "User must authenticate in a real terminal"}, false)
	queue = decodeResult[coordination.Queue](t, callTool(t, c, "work_queue", map[string]any{}, false))
	if len(queue.Jobs) != 1 || queue.Jobs[0].Item.ID != "third" {
		t.Fatalf("human input blocked unrelated job: %+v", queue)
	}
	view := decodeResult[coordination.View](t, callTool(t, c, "work_plan", PlanInput{PlanID: "plan"}, false))
	if view.Plan.Work["second"].PausedBy != "worker" {
		t.Fatal("blocker attribution missing")
	}
	callTool(t, c, "work_review", ReviewInput{PlanID: "plan", RequestID: "self-review", BaseVersion: view.Plan.Version, SubmissionID: sub.SubmissionID, Decision: "accept", Reason: "Not authorized", VerificationRefs: []string{"test"}}, true)
	callTool(t, c, "work_unblock", UnblockInput{PlanID: "plan", RequestID: "self-unblock", BaseVersion: view.Plan.Version, ItemID: "second", Reason: "Not authorized"}, true)
	callTool(t, c, "work_plan", PlanInput{PlanID: "outside"}, true)
	callTool(t, c, "work_queue", QueueInput{WaitSeconds: 21}, true)
	// Idempotent retry returns the original claim, not a second attempt.
	retry := decodeResult[coordination.Receipt](t, callTool(t, c, "work_claim", ClaimInput{PlanID: "plan", RequestID: "claim-first", BaseVersion: 2, ItemID: "first"}, false))
	if retry.AttemptID != claim.AttemptID {
		t.Fatal("retry duplicated work")
	}
	for _, value := range []any{queue, view, tools} {
		body, _ := json.Marshal(value)
		if strings.Contains(string(body), token) {
			t.Fatal("private token exposed")
		}
	}
}

func TestWorkerRejectsUnsafeConfiguration(t *testing.T) {
	base := Config{Endpoint: "http://127.0.0.1:9467", Token: "private", PlanIDs: []string{"plan"}, Machine: "pc", Agent: "claude"}
	for _, mutate := range []func(*Config){func(c *Config) { c.Endpoint = "http://example.com" }, func(c *Config) { c.Endpoint = "https://user:secret@example.com" }, func(c *Config) { c.PlanIDs = nil }, func(c *Config) { c.PlanIDs = []string{"plan", "plan"} }, func(c *Config) { c.Capabilities = []string{"test", "test"} }, func(c *Config) { c.Machine = "not a label" }} {
		c := base
		mutate(&c)
		if _, err := NewServer(c); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
}

func TestWorkerRedactsCredentialEchoAndCancelsBoundedWait(t *testing.T) {
	secret := "lworch_" + strings.Repeat("a", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]string{"error": r.Header.Get("Authorization")})
	}))
	defer srv.Close()
	s, err := NewServer(Config{Endpoint: srv.URL, Token: secret, PlanIDs: []string{"plan"}, Machine: "pc", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	r := callTool(t, connectClient(t, s), "work_queue", map[string]any{}, true)
	body, _ := json.Marshal(r)
	if strings.Contains(string(body), secret) {
		t.Fatal("credential echoed in error")
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(coordination.Queue{SchemaVersion: 1, Jobs: []coordination.QueuedJob{}})
	}))
	defer empty.Close()
	a := &adapter{config: Config{Endpoint: empty.URL, Token: secret, PlanIDs: []string{"plan"}}, profile: coordination.WorkerProfile{OS: runtime.GOOS, Arch: runtime.GOARCH}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, err := a.queue(ctx, nil, QueueInput{WaitSeconds: 20}); err == nil {
		t.Fatal("cancel ignored")
	}
	if time.Since(start) > time.Second {
		t.Fatal("wait did not cancel promptly")
	}
}
