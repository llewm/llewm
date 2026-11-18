package transport

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
)

func TestProjectRegistryIsolation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	tokens := map[string]string{}
	var denied string
	for _, project := range []string{"red", "blue"} {
		s, err := coordination.OpenSharedBroker(filepath.Join(dir, "private", project+".db"), true, project, "host")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		tokens[project], err = s.Enroll(ctx, "alice", []string{"work"})
		if err != nil {
			t.Fatal(err)
		}
		denied, err = s.Enroll(ctx, "bob", []string{"work"})
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "projects.json")
	config := `{"version":1,"projects":[{"id":"red","database":"private/red.db","members":[{"id":"alice"}],"max_events":1},{"id":"blue","database":"private/blue.db","members":[{"id":"alice"}]}]}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	h, closeAll, err := OpenProjects(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	server := httptest.NewTLSServer(h)
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	state := []byte(`{"schema_version":1,"request_id":"start","action":"state","session_id":"same","machine":"client","agent":"test","state":"active","ttl_seconds":60}`)
	ids := map[string]string{}
	var redEvent string
	for _, project := range []string{"red", "blue"} {
		endpoint := server.URL + "/v1/projects/" + project
		data, err := SharedBrokerRequest(ctx, endpoint, tokens[project], project, "POST", "/v1/broker/commands", state, ca)
		if err != nil {
			t.Fatal(err)
		}
		var receipt coordination.BrokerReceipt
		if err = json.Unmarshal(data, &receipt); err != nil {
			t.Fatal(err)
		}
		ids[project] = receipt.BrokerID
		if project == "red" {
			redEvent = receipt.ID
		}
	}
	if ids["red"] == ids["blue"] {
		t.Fatal("projects share authority")
	}
	call := func(project, token, method, suffix, body string) (string, error) {
		b, err := Request(ctx, server.URL, token, method, "/v1/projects/"+project+suffix, []byte(body), ca)
		return string(b), err
	}
	for _, tc := range []struct{ method, suffix, body string }{
		{"GET", "", ""}, {"POST", "/commands", string(state)}, {"POST", "/pull", `{"session_id":"same","wait_seconds":1}`},
	} {
		for _, token := range []string{tokens["red"], denied} {
			if _, err := call("blue", token, tc.method, tc.suffix, tc.body); err == nil || !strings.Contains(err.Error(), "403") {
				t.Fatalf("cross-project or unlisted member: %v", err)
			}
		}
	}
	msg := `{"schema_version":1,"request_id":"send","action":"message","session_id":"same","to_session":"same","text":"blue only"}`
	if _, err := call("red", tokens["red"], "POST", "/commands", msg); err == nil || !strings.Contains(err.Error(), "507") {
		t.Fatalf("quota: %v", err)
	}
	if _, err := call("red", tokens["red"], "POST", "/commands", string(state)); err != nil {
		t.Fatal("retry at quota:", err)
	}
	_, err = call("blue", tokens["blue"], "POST", "/commands", msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"ack", "message"} {
		body := map[string]any{"schema_version": 1, "request_id": "cross-" + action, "session_id": "same", "action": action, "reply_to": redEvent}
		if action == "message" {
			body["text"] = "reply"
		}
		payload, _ := json.Marshal(body)
		if _, err := call("blue", tokens["blue"], "POST", "/commands", string(payload)); err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("foreign reply reference: %v", err)
		}
	}
	feed, err := call("red", tokens["red"], "POST", "/pull", `{"session_id":"same"}`)
	if err != nil || strings.Contains(feed, "blue only") {
		t.Fatalf("cross-project message or pull error: %v", err)
	}
	if _, err := call("blue", tokens["blue"], "POST", "/pull", fmt.Sprintf(`{"session_id":"same","after":1,"broker_id":%q}`, ids["red"])); err == nil {
		t.Fatal("foreign cursor accepted")
	}
	for _, bad := range []string{strings.Replace(config, "private/red.db", "private/blue.db", 1), strings.Replace(config, `"id":"blue"`, `"id":"red"`, 1)} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, cleanup, err := OpenProjects(path); err == nil {
			cleanup()
			t.Fatal("accepted inconsistent registry")
		}
	}
}
