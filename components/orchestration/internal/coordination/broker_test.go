package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type brokerFixture struct {
	s                  *Store
	path               string
	alice, bob, reader string
	now                time.Time
}

func newBrokerFixture(t *testing.T) *brokerFixture {
	t.Helper()
	f := &brokerFixture{path: filepath.Join(t.TempDir(), "private", "local.db"), now: time.Now().UTC()}
	var err error
	f.s, err = OpenBroker(f.path, true, "project", "machine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.s.Close() })
	f.s.now = func() time.Time { return f.now }
	f.alice, err = f.s.Enroll(context.Background(), "alice", []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	f.bob, err = f.s.Enroll(context.Background(), "bob", []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	f.reader, err = f.s.Enroll(context.Background(), "reader", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, who := range []struct{ token, id, task string }{{f.alice, "alice-session", "task-a"}, {f.bob, "bob-session", "task-b"}} {
		_, err = f.s.BrokerExecute(context.Background(), who.token, BrokerCommand{SchemaVersion: 1, RequestID: "start", Action: "state", SessionID: who.id, TaskID: who.task, Agent: "test", State: "active", TTLSeconds: 30})
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func (f *brokerFixture) send(t *testing.T, token string, c BrokerCommand) BrokerReceipt {
	t.Helper()
	c.SchemaVersion = 1
	r, err := f.s.BrokerExecute(context.Background(), token, c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *brokerFixture) pull(t *testing.T, q BrokerQuery) BrokerFeed {
	t.Helper()
	out, err := f.s.BrokerPull(context.Background(), f.bob, q)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestBrokerMessagesRepliesAckRestartAndIsolation(t *testing.T) {
	f := newBrokerFixture(t)
	q := BrokerQuery{SessionID: "bob-session", TaskID: "task-b"}
	first := f.pull(t, q)
	q.After = first.NextAfter
	q.BrokerID = first.Broker.ID
	c := BrokerCommand{RequestID: "verify", Action: "message", SessionID: "alice-session", TaskID: "task-a", ToSession: "bob-session", Text: "Verify checkpoint token=abc123", Refs: []string{"checkpoint:exact"}}
	r := f.send(t, f.alice, c)
	again := f.send(t, f.alice, c)
	if r != again {
		t.Fatal("retry duplicated receipt")
	}
	c.Text = "changed"
	c.SchemaVersion = 1
	if _, err := f.s.BrokerExecute(context.Background(), f.alice, c); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	// An unrelated task-targeted event must not be delivered to Bob.
	f.send(t, f.alice, BrokerCommand{RequestID: "other", Action: "message", SessionID: "alice-session", TaskID: "task-a", ToTask: "task-other", Text: "unrelated"})
	feed := f.pull(t, q)
	if len(feed.Events) != 1 || feed.Events[0].ID != r.ID || feed.Events[0].Actor != "alice" || strings.Contains(feed.Events[0].Command.Text, "abc123") {
		t.Fatalf("routing/redaction: %+v", feed)
	}
	ack := f.send(t, f.bob, BrokerCommand{RequestID: "seen", Action: "ack", SessionID: "bob-session", TaskID: "task-b", ReplyTo: r.ID})
	if ack.Status != "acknowledged" {
		t.Fatal(ack)
	}
	reply := f.send(t, f.bob, BrokerCommand{RequestID: "answer", Action: "message", SessionID: "bob-session", TaskID: "task-b", ReplyTo: r.ID, Text: "All sections present"})
	a, err := f.s.BrokerPull(context.Background(), f.alice, BrokerQuery{SessionID: "alice-session", TaskID: "task-a", ReplyTo: r.ID})
	if err != nil || len(a.Events) != 2 || a.Events[1].ID != reply.ID || a.Events[1].Command.ToTask != "task-a" {
		t.Fatalf("reply: %+v %v", a, err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenBroker(f.path, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	recovered := f.pull(t, BrokerQuery{SessionID: "bob-session", TaskID: "task-b", BrokerID: q.BrokerID, After: q.After})
	if recovered.Broker.ID != first.Broker.ID || recovered.Events[0].ID != r.ID {
		t.Fatal("restart lost identity/history")
	}
	if _, err = Open(f.path, false); err == nil {
		t.Fatal("team opened local store")
	}
	teamPath := filepath.Join(t.TempDir(), "private", "team.db")
	team, err := Open(teamPath, true)
	if err != nil {
		t.Fatal(err)
	}
	team.Close()
	if _, err = OpenBroker(teamPath, false, "", ""); err == nil {
		t.Fatal("broker opened team store")
	}
	// No credential payload persists in the database, journals or returned event.
	for _, path := range []string{f.path, f.path + "-wal"} {
		b, e := os.ReadFile(path)
		if e == nil && strings.Contains(string(b), "abc123") {
			t.Fatal("raw secret persisted")
		}
	}
}
func TestBrokerPermissionsStateAndCursor(t *testing.T) {
	f := newBrokerFixture(t)
	ctx := context.Background()
	command := BrokerCommand{SchemaVersion: 1, RequestID: "spoof", Action: "state", SessionID: "alice-session", Agent: "bob", State: "active", TTLSeconds: 30}
	if _, err := f.s.BrokerExecute(ctx, f.bob, command); !errors.Is(err, ErrForbidden) {
		t.Fatalf("session spoof: %v", err)
	}
	command.SessionID = "reader-session"
	if _, err := f.s.BrokerExecute(ctx, f.reader, command); !errors.Is(err, ErrForbidden) {
		t.Fatal("reader wrote")
	}
	if _, err := f.s.BrokerPull(ctx, f.alice, BrokerQuery{SessionID: "bob-session", TaskID: "task-b"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("foreign inbox cursor")
	}
	q := BrokerQuery{SessionID: "bob-session", TaskID: "task-b"}
	feed := f.pull(t, q)
	q.After = feed.NextAfter
	q.BrokerID = "wrong"
	if _, err := f.s.BrokerPull(ctx, f.bob, q); err == nil {
		t.Fatal("foreign broker cursor accepted")
	}
	q.BrokerID = feed.Broker.ID
	q.After++
	if _, err := f.s.BrokerPull(ctx, f.bob, q); err == nil {
		t.Fatal("future cursor accepted")
	}
	f.now = f.now.Add(time.Minute)
	feed = f.pull(t, BrokerQuery{SessionID: "bob-session", TaskID: "task-b", WatchSession: "alice-session", After: 2, BrokerID: feed.Broker.ID})
	if feed.Reason != "contact_expired" || len(feed.Sessions) != 1 || feed.Sessions[0].State != "expired" {
		t.Fatalf("expiry %+v", feed)
	}
	f.send(t, f.alice, BrokerCommand{RequestID: "end", Action: "state", SessionID: "alice-session", TaskID: "task-a", Agent: "test", State: "ended", TTLSeconds: 30})
	feed = f.pull(t, BrokerQuery{SessionID: "bob-session", TaskID: "task-b"})
	last := feed.Sessions[0]
	if last.State != "ended" {
		t.Fatalf("ended session: %+v", last)
	}
	command.RequestID = "revive"
	command.SessionID = "alice-session"
	if _, err := f.s.BrokerExecute(ctx, f.alice, command); err == nil {
		t.Fatal("ended session revived")
	}
}
func TestBrokerPaginationDoesNotLoseEvents(t *testing.T) {
	f := newBrokerFixture(t)
	q := BrokerQuery{SessionID: "bob-session", TaskID: "task-b"}
	initial := f.pull(t, q)
	q.After = initial.NextAfter
	q.BrokerID = initial.Broker.ID
	for i := range 105 {
		f.send(t, f.alice, BrokerCommand{RequestID: strings.Repeat("x", i+1), Action: "message", SessionID: "alice-session", TaskID: "task-a", ToTask: "task-b", Text: "event"})
	}
	page := f.pull(t, q)
	if len(page.Events) != 100 {
		t.Fatal(len(page.Events))
	}
	q.After = page.NextAfter
	tail := f.pull(t, q)
	if len(tail.Events) != 5 || tail.Events[0].Sequence != page.NextAfter+1 {
		t.Fatal("pagination skipped events")
	}
}
func TestBrokerWaitSeesOtherProcessAndCancels(t *testing.T) {
	f := newBrokerFixture(t)
	other, err := OpenBroker(f.path, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	q := BrokerQuery{SessionID: "bob-session", TaskID: "task-b"}
	first := f.pull(t, q)
	q.After = first.NextAfter
	q.BrokerID = first.Broker.ID
	q.WaitSeconds = 2
	result := make(chan BrokerFeed, 1)
	fail := make(chan error, 1)
	go func() {
		out, e := f.s.BrokerPull(context.Background(), f.bob, q)
		if e != nil {
			fail <- e
		} else {
			result <- out
		}
	}()
	_, err = other.BrokerExecute(context.Background(), f.alice, BrokerCommand{SchemaVersion: 1, RequestID: "wake", Action: "message", SessionID: "alice-session", TaskID: "task-a", ToSession: "bob-session", Text: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-fail:
		t.Fatal(e)
	case out := <-result:
		if len(out.Events) != 1 {
			t.Fatal("wait lost reply")
		}
		q.After = out.NextAfter
	case <-time.After(3 * time.Second):
		t.Fatal("wait hung")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = f.s.BrokerPull(ctx, f.bob, q); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	q.WaitSeconds = 1
	out, err := f.s.BrokerPull(context.Background(), f.bob, q)
	if err != nil || out.Reason != "timeout" {
		t.Fatalf("timeout: %+v %v", out, err)
	}
	if err = other.Revoke(context.Background(), "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.BrokerPull(context.Background(), f.bob, q); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked reader accepted")
	}
}
func TestBrokerRejectsUnboundedAndAmbiguousCommands(t *testing.T) {
	f := newBrokerFixture(t)
	base := BrokerCommand{SchemaVersion: 1, RequestID: "bad", Action: "message", SessionID: "alice-session", TaskID: "task-a", Text: "hello"}
	for _, change := range []func(*BrokerCommand){func(c *BrokerCommand) { c.Text = strings.Repeat("x", 16385) }, func(c *BrokerCommand) { c.ToTask = "task-b"; c.ToSession = "bob-session" }, func(c *BrokerCommand) { c.ReplyTo = "unknown" }, func(c *BrokerCommand) { c.Action = "claim" }, func(c *BrokerCommand) { c.Refs = []string{""} }} {
		c := base
		change(&c)
		if _, err := f.s.BrokerExecute(context.Background(), f.alice, c); err == nil {
			b, _ := json.Marshal(c)
			t.Fatalf("accepted %s", b)
		}
	}
}

func TestBrokerTaskRepliesSurviveSenderRestart(t *testing.T) {
	f := newBrokerFixture(t)
	request := f.send(t, f.alice, BrokerCommand{RequestID: "ask", Action: "message", SessionID: "alice-session", TaskID: "task-a", ToTask: "task-b", Text: "verify"})
	f.send(t, f.alice, BrokerCommand{RequestID: "end", Action: "state", SessionID: "alice-session", TaskID: "task-a", Agent: "test", State: "ended", TTLSeconds: 30})
	f.send(t, f.alice, BrokerCommand{RequestID: "restart", Action: "state", SessionID: "alice-new", TaskID: "task-a", Agent: "test", State: "active", TTLSeconds: 30})
	reply := f.send(t, f.bob, BrokerCommand{RequestID: "reply", Action: "message", SessionID: "bob-session", TaskID: "task-b", ReplyTo: request.ID, Text: "verified"})
	feed, err := f.s.BrokerPull(context.Background(), f.alice, BrokerQuery{SessionID: "alice-new", TaskID: "task-a", ReplyTo: request.ID})
	if err != nil || len(feed.Events) != 1 || feed.Events[0].ID != reply.ID {
		t.Fatalf("task reply lost: %+v %v", feed, err)
	}
}

func TestBrokerWaitRechecksRevocationAndCancellation(t *testing.T) {
	f := newBrokerFixture(t)
	other, err := OpenBroker(f.path, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	first := f.pull(t, BrokerQuery{SessionID: "bob-session", TaskID: "task-b"})
	q := BrokerQuery{SessionID: "bob-session", TaskID: "task-b", BrokerID: first.Broker.ID, After: first.NextAfter, WaitSeconds: 5}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := f.s.BrokerPull(ctx, f.bob, q); done <- err }()
	// Cancellation must interrupt an established wait, not only a pre-cancelled request.
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release wait")
	}
	go func() { _, err := f.s.BrokerPull(context.Background(), f.bob, q); done <- err }()
	time.Sleep(25 * time.Millisecond)
	if err := other.Revoke(context.Background(), "bob"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrForbidden) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("revoked waiter continued")
	}
}
