package transport

import (
	"context"
	"encoding/json"
	"encoding/pem"
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

func TestSharedBrokerTLSAndDestinationChecks(t *testing.T) {
	ctx := context.Background()
	for _, scope := range []string{"machine", "shared"} {
		t.Run(scope, func(t *testing.T) {
			open := coordination.OpenBroker
			if scope == "shared" {
				open = coordination.OpenSharedBroker
			}
			s, err := open(filepath.Join(t.TempDir(), "private", "broker.db"), true, "project", "host")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			token, err := s.Enroll(ctx, "engineer", []string{"work"})
			if err != nil {
				t.Fatal(err)
			}
			posts := 0
			handler := BrokerHandler(s)
			h := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
				}
				handler.ServeHTTP(w, r)
			}))
			defer h.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.Certificate().Raw})
			body := []byte(`{"schema_version":1,"request_id":"start","action":"state","session_id":"s","agent":"a","machine":"client","state":"active","ttl_seconds":60}`)
			for _, test := range []struct {
				project, credential string
				ca                  []byte
			}{
				{"project", token, nil}, {"wrong-project", token, ca}, {"project", "invalid", ca},
			} {
				if _, err := SharedBrokerRequest(ctx, h.URL, test.credential, test.project, "POST", "/v1/broker/commands", body, test.ca); err == nil {
					t.Fatal("untrusted destination accepted")
				}
			}
			if posts != 0 {
				t.Fatal("sent content before destination validation")
			}
			_, err = SharedBrokerRequest(ctx, h.URL, token, "project", "POST", "/v1/broker/commands", body, ca)
			if scope == "machine" {
				if err == nil || posts != 0 {
					t.Fatal("sent shared content to local authority")
				}
			} else if err != nil || posts != 1 {
				t.Fatalf("shared request: %d %v", posts, err)
			}
		})
	}
	for _, endpoint := range []string{"http://127.0.0.1:9469", "http://example.com:9469", "https://example.com/path", "https://user@example.com"} {
		if err := ValidateSharedBrokerEndpoint(endpoint); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
}
