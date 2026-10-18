package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
)

func server(t *testing.T) (string, string) {
	t.Helper()
	s, err := coordination.Open(filepath.Join(t.TempDir(), "private", "team.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	token, err := s.Enroll(context.Background(), "dave", []string{"plan", "accept", "assign", "review", "work"})
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(Handler(s))
	t.Cleanup(h.Close)
	return h.URL, token
}

func TestHTTPProtocolAuthenticationAndRetry(t *testing.T) {
	endpoint, token := server(t)
	c := coordination.Command{SchemaVersion: 1, RequestID: "proposal", PlanID: "plan", Action: "propose", Definition: &coordination.Definition{Title: "Team plan", Items: []coordination.Item{{ID: "task", Title: "Implement", AcceptanceCriteria: "Tests pass"}}}}
	body, _ := json.Marshal(c)
	if _, err := Request(context.Background(), endpoint, "invalid", "POST", "/v1/commands", body); err == nil {
		t.Fatal("unauthenticated mutation succeeded")
	}
	first, err := Request(context.Background(), endpoint, token, "POST", "/v1/commands", body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Request(context.Background(), endpoint, token, "POST", "/v1/commands", body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("retry receipt changed")
	}
	read, err := Request(context.Background(), endpoint, token, "GET", "/v1/plans/plan", nil)
	if err != nil {
		t.Fatal(err)
	}
	var v coordination.View
	if err := json.Unmarshal(read, &v); err != nil {
		t.Fatal(err)
	}
	if v.Plan.Revisions[0].Author != "dave" || v.Plan.Version != 1 {
		t.Fatal("authenticated attribution missing")
	}
	list, err := Request(context.Background(), endpoint, token, "GET", "/v1/plans", nil)
	if err != nil || !strings.Contains(string(list), "Team plan") {
		t.Fatalf("list: %s %v", list, err)
	}
	for _, invalid := range []string{`{"actor":"dave"}`, `{} {}`, strings.Repeat("x", coordination.MaxCommandBytes+1)} {
		req, _ := http.NewRequest("POST", endpoint+"/v1/commands", strings.NewReader(invalid))
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("invalid input accepted: %d", response.StatusCode)
		}
	}
}

func TestClientRejectsUnsafeOriginsAndRedirects(t *testing.T) {
	for _, endpoint := range []string{"http://example.com", "file:///tmp/test", "https://user:pass@example.com", "https://example.com/path", "https://example.com?token=secret"} {
		if _, err := Request(context.Background(), endpoint, "unused", "GET", "/v1/plans", nil); err == nil {
			t.Fatalf("unsafe origin accepted: %s", endpoint)
		}
	}
	called := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer source.Close()
	if _, err := Request(context.Background(), source.URL, "unused", "GET", "/v1/plans", nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if called {
		t.Fatal("redirect destination received credential-bearing request")
	}
	if !Loopback("127.0.0.1:9467") || !Loopback("[::1]:9467") || Loopback("0.0.0.0:9467") || Loopback(":9467") {
		t.Fatal("loopback validation")
	}
}
