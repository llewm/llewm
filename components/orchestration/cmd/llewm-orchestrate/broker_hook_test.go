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

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/privatestorage"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
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
		return brokerHook(context.Background(), h.URL, tokenPath, "codex", "task", "active", cursor, strings.NewReader(envelope), w)
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
