package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
)

func TestBrokerHTTPBoundaries(t *testing.T) {
	store, err := coordination.OpenBroker(filepath.Join(t.TempDir(), "private", "broker.db"), true, "project", "machine")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, err := store.Enroll(context.Background(), "engineer", []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(BrokerHandler(store))
	defer h.Close()
	for _, path := range []string{"/v1/plans", "/v1/broker"} {
		if _, err := Request(context.Background(), h.URL, "invalid", "GET", path, nil, nil); err == nil {
			t.Fatal("unauthenticated read or team route exposed")
		}
	}
	if _, err := Request(context.Background(), h.URL, token, "POST", "/v1/commands", []byte(`{}`), nil); err == nil {
		t.Fatal("team command exposed")
	}
	command := coordination.BrokerCommand{SchemaVersion: 1, RequestID: "start", Action: "state", SessionID: "session", Agent: "test", State: "active", TTLSeconds: 60}
	b, _ := json.Marshal(command)
	if _, err := Request(context.Background(), h.URL, token, "POST", "/v1/broker/commands", b, nil); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"actor":"spoof"}`, `{} {}`, strings.Repeat("x", coordination.MaxCommandBytes+1)} {
		req, _ := http.NewRequest("POST", h.URL+"/v1/broker/commands", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatal(response.StatusCode)
		}
	}
	for _, endpoint := range []string{"https://example.com:9468", "http://localhost:9468", "http://127.0.0.1:9468/path", "http://127.0.0.1"} {
		if ValidateLocalEndpoint(endpoint) == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	if err := ValidateLocalEndpoint(h.URL); err != nil {
		t.Fatal(err)
	}
}
