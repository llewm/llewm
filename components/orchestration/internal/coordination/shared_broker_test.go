package coordination

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedBrokerRecoveryAttributionAndIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "shared.db")
	s, err := OpenSharedBroker(path, true, "project", "server")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	alice, err := s.Enroll(ctx, "alice", []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.Enroll(ctx, "bob", []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	state := BrokerCommand{SchemaVersion: 1, RequestID: "start", Action: "state", SessionID: "alice-session", Agent: "codex", State: "active", TTLSeconds: 60}
	if _, err := s.BrokerExecute(ctx, alice, state); err == nil {
		t.Fatal("missing machine accepted")
	}
	state.Machine = "alice-host"
	if _, err := s.BrokerExecute(ctx, alice, state); err != nil {
		t.Fatal(err)
	}
	state.SessionID, state.Machine = "bob-session", "bob-host"
	if _, err := s.BrokerExecute(ctx, bob, state); err != nil {
		t.Fatal(err)
	}
	state.RequestID, state.Machine = "move", "other-host"
	if _, err := s.BrokerExecute(ctx, bob, state); err == nil {
		t.Fatal("machine silently changed")
	}
	state.Machine = "bob-host"
	if _, err := s.BrokerExecute(ctx, alice, state); !errors.Is(err, ErrForbidden) {
		t.Fatalf("session hijack: %v", err)
	}
	query := BrokerQuery{SessionID: "bob-session"}
	before, err := s.BrokerPull(ctx, bob, query)
	if err != nil {
		t.Fatal(err)
	}
	query.BrokerID, query.After = before.Broker.ID, before.NextAfter
	c := BrokerCommand{SchemaVersion: 1, RequestID: "message", Action: "message", SessionID: "alice-session", ToSession: "bob-session", Text: "Review token=hidden", Refs: []string{"commit:abc"}}
	r, err := s.BrokerExecute(ctx, alice, c)
	if err != nil || r.Status != "shared_stored" {
		t.Fatalf("%+v %v", r, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSharedBroker(path, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.BrokerExecute(ctx, alice, c)
	if err != nil || retry != r {
		t.Fatalf("retry after restart: %+v %v", retry, err)
	}
	feed, err := s.BrokerPull(ctx, bob, query)
	if err != nil || feed.Broker.Scope != "shared" || len(feed.Events) != 1 || feed.Events[0].ID != r.ID || feed.Events[0].Actor != "alice" || strings.Contains(feed.Events[0].Command.Text, "hidden") {
		t.Fatalf("recovery: %+v %v", feed, err)
	}
	if _, err := s.BrokerPull(ctx, alice, query); !errors.Is(err, ErrForbidden) {
		t.Fatalf("inbox hijack: %v", err)
	}
	if err := s.Revoke(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BrokerPull(ctx, bob, query); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revocation: %v", err)
	}
	for _, open := range []func(string, bool, string, string) (*Store, error){OpenBroker, func(p string, create bool, _, _ string) (*Store, error) { return Open(p, create) }} {
		other, err := open(path, false, "", "")
		if err == nil {
			other.Close()
			t.Fatal("shared store opened with another authority")
		}
	}
	localPath := filepath.Join(t.TempDir(), "private", "local.db")
	local, err := OpenBroker(localPath, true, "project", "host")
	if err != nil {
		t.Fatal(err)
	}
	local.Close()
	if other, err := OpenSharedBroker(localPath, false, "", ""); err == nil {
		other.Close()
		t.Fatal("local store exposed as shared")
	}
}
