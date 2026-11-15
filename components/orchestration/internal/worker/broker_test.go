package worker

import (
	"context"
	"encoding/pem"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBrokerMCPRequestReplyAndAcknowledgment(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "local"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) { testBrokerMCPRequestReplyAndAcknowledgment(t, shared) })
	}
}

func testBrokerMCPRequestReplyAndAcknowledgment(t *testing.T, shared bool) {
	ctx := context.Background()
	open := coordination.OpenBroker
	if shared {
		open = coordination.OpenSharedBroker
	}
	store, err := open(filepath.Join(t.TempDir(), "private", "broker.db"), true, "project", "machine")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, err := store.Enroll(ctx, "engineer", []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	bobToken, err := store.Enroll(ctx, "bob", []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(transport.BrokerHandler(store))
	var ca []byte
	if shared {
		httpServer.StartTLS()
		ca = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: httpServer.Certificate().Raw})
	} else {
		httpServer.Start()
	}
	defer httpServer.Close()
	newClient := func(session, task string) *mcp.ClientSession {
		credential := token
		if session == "bob" {
			credential = bobToken
		}
		config := BrokerConfig{Endpoint: httpServer.URL, Token: credential, SessionID: session, TaskID: task, Agent: "test", Project: "project", Machine: session + "-host", CAPEM: ca}
		newServer := NewBrokerServer
		if shared {
			newServer = NewSharedBrokerServer
		}
		s, e := newServer(config)
		if e != nil {
			t.Fatal(e)
		}
		return connectClient(t, s)
	}
	alice := newClient("alice", "task-a")
	bob := newClient("bob", "task-b")
	tools, err := alice.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 5 {
		t.Fatalf("tools %v %v", tools, err)
	}
	for _, c := range []*mcp.ClientSession{alice, bob} {
		callTool(t, c, "broker_state", BrokerStateInput{RequestID: "start-" + map[*mcp.ClientSession]string{alice: "a", bob: "b"}[c], State: "active", TTLSeconds: 60, Summary: "current scope"}, false)
	}
	info := decodeResult[coordination.BrokerIdentity](t, callTool(t, alice, "broker_info", InfoInput{}, false))
	scope := "machine"
	if shared {
		scope = "shared"
	}
	if info.Scope != scope || info.Project != "project" {
		t.Fatal(info)
	}
	callTool(t, bob, "broker_pull", map[string]any{"session_id": "alice", "after": 0}, true)
	request := decodeResult[coordination.BrokerReceipt](t, callTool(t, alice, "broker_send", BrokerSendInput{RequestID: "verify", ToTask: "task-b", Text: "Read exact checkpoint", Refs: []string{"checkpoint:exact"}}, false))
	if shared && request.Status != "shared_stored" {
		t.Fatal(request)
	}
	received := decodeResult[coordination.BrokerFeed](t, callTool(t, bob, "broker_pull", BrokerPullInput{}, false))
	found := false
	for _, event := range received.Events {
		if event.ID == request.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("message missing")
	}
	if shared {
		for _, session := range received.Sessions {
			if session.Machine != session.ID+"-host" {
				t.Fatal(session)
			}
		}
	}
	callTool(t, bob, "broker_ack", BrokerAckInput{RequestID: "ack", ReplyTo: request.ID}, false)
	callTool(t, bob, "broker_send", BrokerSendInput{RequestID: "reply", ReplyTo: request.ID, Text: "Fully recovered; own task unchanged"}, false)
	replies := decodeResult[coordination.BrokerFeed](t, callTool(t, alice, "broker_pull", BrokerPullInput{ReplyTo: request.ID}, false))
	if len(replies.Events) != 2 {
		t.Fatal(replies)
	}
	callTool(t, alice, "broker_send", BrokerSendInput{RequestID: "bad", ReplyTo: request.ID, Text: "spoof reply"}, true)
	callTool(t, alice, "broker_state", BrokerStateInput{RequestID: "end", State: "ended", TTLSeconds: 60}, false)
	callTool(t, alice, "broker_state", BrokerStateInput{RequestID: "late", State: "active", TTLSeconds: 60}, true)
	callTool(t, alice, "broker_state", BrokerStateInput{RequestID: "resume", State: "active", Resume: true, TTLSeconds: 60}, false)
	callTool(t, alice, "broker_send", BrokerSendInput{RequestID: "resumed-send", ToTask: "task-b", Text: "Resumed same session"}, false)

}

func TestBrokerMCPRefusesRemoteAndInvalidIdentity(t *testing.T) {
	for _, c := range []BrokerConfig{{Endpoint: "https://example.com:443", Token: "fixture", SessionID: "s", Agent: "a"}, {Endpoint: "http://127.0.0.1:9468", Token: "fixture", SessionID: "bad/id", Agent: "a"}} {
		if _, err := NewBrokerServer(c); err == nil {
			t.Fatal("invalid broker configuration accepted")
		}
	}
}

func TestSharedBrokerMCPRejectsMissingScopeAndPlaintext(t *testing.T) {
	for _, c := range []BrokerConfig{
		{Endpoint: "http://127.0.0.1:9469", Token: "fixture", Project: "project", Machine: "host", SessionID: "s", Agent: "a"},
		{Endpoint: "https://example.com:9469", Token: "fixture", Machine: "host", SessionID: "s", Agent: "a"},
		{Endpoint: "https://example.com:9469", Token: "fixture", Project: "project", SessionID: "s", Agent: "a"},
	} {
		if _, err := NewSharedBrokerServer(c); err == nil {
			t.Fatal("invalid shared configuration accepted")
		}
	}
}
