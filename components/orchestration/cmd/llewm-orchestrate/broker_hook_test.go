package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
	"github.com/llewm/llewm/components/privatestorage"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }
func TestBrokerCLIAndHookCycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	db := filepath.Join(dir, "broker.db")
	tokenPath := filepath.Join(dir, "engineer.token")
	var out bytes.Buffer
	for _, args := range [][]string{{"broker-init", "--db", db, "--project", "project", "--machine", "machine"}, {"broker-member", "--db", db, "--id", "engineer", "--permissions", "work", "--token-file", tokenPath}} {
		if err := run(args, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	store, err := coordination.OpenBroker(db, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, err := readPrivateToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(transport.BrokerHandler(store))
	defer h.Close()
	cursor := filepath.Join(dir, "session.cursor")
	envelope := `{"session_id":"session","hook_event_name":"SessionStart","tool_input":{"token":"never-persist-me"}}`
	hook := func(envelope string, w io.Writer) error {
		return brokerHook(context.Background(), h.URL, tokenPath, "codex", "task", "active", cursor, "", strings.NewReader(envelope), w)
	}
	out.Reset()
	if err = hook(envelope, &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("self heartbeat notified itself")
	}
	_, err = store.BrokerExecute(context.Background(), token, coordination.BrokerCommand{SchemaVersion: 1, RequestID: "other-start", Action: "state", SessionID: "other", Agent: "claude", State: "active", TTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BrokerExecute(context.Background(), token, coordination.BrokerCommand{SchemaVersion: 1, RequestID: "message", Action: "message", SessionID: "other", ToSession: "session", Text: "Verify checkpoint"})
	if err != nil {
		t.Fatal(err)
	}
	if err = hook(envelope, failingWriter{}); err == nil {
		t.Fatal("failed output advanced cursor")
	}
	if err = hook(envelope, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "broker_pull") {
		t.Fatal("notification missing")
	}
	out.Reset()
	if err = hook(envelope, &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("notification cursor repeated")
	}
	// Another session's heartbeat alone is not worth a hint.
	_, err = store.BrokerExecute(context.Background(), token, coordination.BrokerCommand{SchemaVersion: 1, RequestID: "other-beat", Action: "state", SessionID: "other", Agent: "claude", State: "active", TTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if err = hook(envelope, &out); err != nil || out.Len() != 0 {
		t.Fatalf("state heartbeat hinted: %v %q", err, out.String())
	}
	// Scope published by the agent survives blank hook heartbeats.
	_, err = store.BrokerExecute(context.Background(), token, coordination.BrokerCommand{SchemaVersion: 1, RequestID: "scope", Action: "state", SessionID: "session", TaskID: "task", Agent: "codex", State: "active", TTLSeconds: 60, Text: "Editing orchestration", Refs: []string{"file:components/orchestration"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = hook(envelope, &out); err != nil {
		t.Fatal(err)
	}
	feed, err := store.BrokerPull(context.Background(), token, coordination.BrokerQuery{SessionID: "session", TaskID: "task", WatchSession: "session"})
	if err != nil || feed.Sessions[0].Summary != "Editing orchestration" {
		t.Fatalf("scope erased: %+v %v", feed, err)
	}
	b, err := os.ReadFile(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "never-persist-me") {
		t.Fatal("raw envelope persisted")
	}
	f, err := privatestorage.Open(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if err = privatestorage.CheckFile(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = hook(strings.Replace(envelope, "session\"", "other\"", 1), &out); err == nil {
		t.Fatal("foreign session reused cursor")
	}
	// Busy locks wait within the caller's budget and report a timeout explicitly.
	before, _ := os.ReadFile(cursor)
	held, err := privatestorage.Lock(context.Background(), cursor+".lock")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err = brokerHook(ctx, h.URL, tokenPath, "codex", "task", "active", cursor, "", strings.NewReader(envelope), &out)
	cancel()
	held.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy lock: %v", err)
	}
	if after, _ := os.ReadFile(cursor); !bytes.Equal(before, after) {
		t.Fatal("busy lock advanced cursor")
	}
	// A cursor directory derives one private cursor per envelope session.
	cursors := filepath.Join(dir, "cursors")
	byDir := func(envelope string) error {
		return brokerHook(context.Background(), h.URL, tokenPath, "claude", "", "active", "", cursors, strings.NewReader(envelope), io.Discard)
	}
	for _, id := range []string{"dir-a", "dir-b", "dir.c"} {
		if err = byDir(`{"session_id":"` + id + `","hook_event_name":"PostToolUse"}`); err != nil {
			t.Fatal(id, err)
		}
	}
	entries, _ := os.ReadDir(cursors)
	var names []os.DirEntry
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".cursor") {
			names = append(names, entry)
		}
	}
	if len(names) != 3 || names[0].Name() != "dir-a.project.cursor" || names[1].Name() != "dir-b.project.cursor" || !strings.HasPrefix(names[2].Name(), "h") {
		t.Fatalf("derived cursors: %v", names)
	}
	if err = byDir(`{"hook_event_name":"PostToolUse"}`); err == nil {
		t.Fatal("cursor directory accepted a missing session_id")
	}
	if err = brokerHook(context.Background(), h.URL, tokenPath, "claude", "", "active", cursor, cursors, strings.NewReader(envelope), io.Discard); err == nil {
		t.Fatal("both cursor flags accepted")
	}
	// CLI exposes the same protocol and never logs credentials.
	out.Reset()
	if err = run([]string{"broker-info", "--endpoint", h.URL, "--token-file", tokenPath}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var identity coordination.BrokerIdentity
	if err = json.Unmarshal(out.Bytes(), &identity); err != nil || identity.Scope != "machine" {
		t.Fatal("identity missing")
	}
	if strings.Contains(out.String(), token) {
		t.Fatal("credential leaked")
	}
	for _, cmd := range []string{"broker-request", "broker-pull"} {
		path := filepath.Join(dir, cmd+".json")
		payload := `{"session_id":"session","task_id":"task","after":0}`
		if cmd == "broker-request" {
			payload = `{"schema_version":1,"request_id":"cli-message","action":"message","session_id":"session","task_id":"task","to_session":"other","text":"hello"}`
		}
		if err = os.WriteFile(path, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		if err = run([]string{cmd, "--endpoint", h.URL, "--token-file", tokenPath, "--file", path}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("LLEWM_TEST_BROKER_SESSION", "")
	if err = run([]string{"broker-mcp", "--endpoint", h.URL, "--token-file", tokenPath, "--agent", "claude", "--session-env", "LLEWM_TEST_BROKER_SESSION"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "LLEWM_TEST_BROKER_SESSION") {
		t.Fatalf("empty session env accepted: %v", err)
	}
	if err = run([]string{"broker-serve", "--db", db, "--listen", "0.0.0.0:9468"}, io.Discard, io.Discard); err == nil {
		t.Fatal("nonlocal listener accepted")
	}
	if err = hook(strings.Replace(envelope, "SessionStart", "SessionEnd", 1), io.Discard); err != nil {
		t.Fatal(err)
	}
	feed, err = store.BrokerPull(context.Background(), token, coordination.BrokerQuery{SessionID: "session", TaskID: "task", WatchSession: "session"})
	if err != nil || feed.Sessions[0].State != "ended" {
		t.Fatal("end not observed")
	}
}

func TestBrokerHookPreservesTaskAndSerializesEndResume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	db, tokenPath := filepath.Join(dir, "broker.db"), filepath.Join(dir, "token")
	for _, args := range [][]string{{"broker-init", "--db", db, "--project", "project", "--machine", "machine"}, {"broker-member", "--db", db, "--id", "engineer", "--permissions", "work", "--token-file", tokenPath}} {
		if err := run(args, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	store, err := coordination.OpenBroker(db, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, err := readPrivateToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(transport.BrokerHandler(store))
	defer server.Close()
	cursor := filepath.Join(dir, "session.cursor")
	hook := func(event string, out io.Writer) error {
		return brokerHook(context.Background(), server.URL, tokenPath, "codex", "", "active", cursor, "", strings.NewReader(`{"session_id":"session","hook_event_name":"`+event+`"}`), out)
	}
	send := func(c coordination.BrokerCommand) coordination.BrokerReceipt {
		t.Helper()
		c.SchemaVersion = 1
		receipt, err := store.BrokerExecute(context.Background(), token, c)
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	if err = hook("SessionStart", io.Discard); err != nil {
		t.Fatal(err)
	}
	send(coordination.BrokerCommand{RequestID: "peer-start", Action: "state", SessionID: "peer", Agent: "claude", State: "active", TTLSeconds: 60})
	// Store a task-addressed message before this task is bound. The previous
	// task-less cursor will advance beyond it, so rebinding must reset that cursor.
	message := send(coordination.BrokerCommand{RequestID: "offline", Action: "message", SessionID: "peer", ToTask: "task", Text: "Check this task"})
	if err = hook("PostToolUse", io.Discard); err != nil {
		t.Fatal(err)
	}
	send(coordination.BrokerCommand{RequestID: "task", Action: "state", SessionID: "session", TaskID: "task", Agent: "codex", State: "active", TTLSeconds: 60, Text: "Working", Refs: []string{"file:work"}})
	var out bytes.Buffer
	if err = hook("PostToolUse", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "broker_pull") {
		t.Fatal("task message hint missing after scope change")
	}
	query := coordination.BrokerQuery{SessionID: "session", TaskID: "task", ReplyTo: ""}
	feed, err := store.BrokerPull(context.Background(), token, query)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range feed.Events {
		if e.ID == message.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("task message lost")
	}
	for _, session := range feed.Sessions {
		if session.ID == "session" && (session.TaskID != "task" || session.Summary != "Working" || len(session.Refs) != 1) {
			t.Fatalf("scope erased: %+v", session)
		}
	}
	beforeEnd, err := os.ReadFile(cursor)
	if err != nil {
		t.Fatal(err)
	}
	held, err := privatestorage.Lock(context.Background(), cursor+".lock")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- hook("SessionEnd", io.Discard) }()
	select {
	case err := <-done:
		held.Close()
		t.Fatalf("SessionEnd did not wait: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	held.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if afterEnd, err := os.ReadFile(cursor); err != nil || !bytes.Equal(beforeEnd, afterEnd) {
		t.Fatalf("SessionEnd consumed unread notifications: %v", err)
	}
	query.WatchSession = "session"
	feed, err = store.BrokerPull(context.Background(), token, query)
	if err != nil || feed.Sessions[0].State != "ended" {
		t.Fatalf("end lost: %+v %v", feed, err)
	}
	if err = hook("PostToolUse", io.Discard); err == nil {
		t.Fatal("late heartbeat revived ended session")
	}
	if err = hook("SessionStart", io.Discard); err != nil {
		t.Fatal(err)
	}
	feed, err = store.BrokerPull(context.Background(), token, query)
	if err != nil || feed.Sessions[0].State != "active" || feed.Sessions[0].Summary != "Working" {
		t.Fatalf("resume lost scope: %+v %v", feed, err)
	}
	send(coordination.BrokerCommand{RequestID: "reply", Action: "message", SessionID: "session", TaskID: "task", ReplyTo: message.ID, Text: "Resumed and received"})
}
