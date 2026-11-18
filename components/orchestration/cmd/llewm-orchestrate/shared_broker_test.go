package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/orchestration/internal/connectionprofile"
	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
)

func TestSharedBrokerProxyListenerPolicy(t *testing.T) {
	for _, address := range []string{"0.0.0.0:9469", "[::]:9469", "192.0.2.1:9469", "localhost:9469", ":9469"} {
		if err := validateSharedListener(address, "", "", true); err == nil {
			t.Fatalf("plaintext public backend accepted: %s", address)
		}
	}
	for _, address := range []string{"127.0.0.1:9469", "[::1]:9469"} {
		if err := validateSharedListener(address, "", "", true); err != nil {
			t.Fatal(err)
		}
		if err := validateSharedListener(address, "", "", false); err == nil {
			t.Fatal("implicit plaintext accepted")
		}
		if err := validateSharedListener(address, "cert", "key", true); err == nil {
			t.Fatal("ambiguous TLS/proxy configuration accepted")
		}
	}
	for _, cmd := range []string{"broker-serve", "serve", "shared-broker-request"} {
		var out bytes.Buffer
		if err := run([]string{cmd, "--proxy-loopback"}, &out, &out); err == nil {
			t.Fatalf("proxy option accepted for %s", cmd)
		}
	}
}

func TestSharedBrokerCLI(t *testing.T) {
	dir := t.TempDir()
	db, token := filepath.Join(dir, "private", "shared.db"), filepath.Join(dir, "engineer.token")
	invoke := func(args ...string) (string, error) {
		var out, stderr bytes.Buffer
		err := run(args, &out, &stderr)
		return out.String(), err
	}
	for _, args := range [][]string{
		{"shared-broker-init", "--db", db, "--project", "project", "--machine", "server"},
		{"shared-broker-member", "--db", db, "--id", "engineer", "--permissions", "work", "--token-file", token},
	} {
		out, err := invoke(args...)
		if err != nil || strings.Contains(out, "lworch_") {
			t.Fatalf("%v: %s %v", args, out, err)
		}
	}
	for _, address := range []string{"127.0.0.1:9469", "0.0.0.0:9469"} {
		if _, err := invoke("shared-broker-serve", "--db", db, "--listen", address); err == nil || !strings.Contains(err.Error(), "requires --tls-cert") {
			t.Fatalf("plaintext shared listener: %v", err)
		}
	}
	if _, err := invoke("broker-serve", "--db", db); err == nil {
		t.Fatal("local server opened shared db")
	}
	if _, err := invoke("shared-broker-hook"); err == nil {
		t.Fatal("unreviewed hook forwarding accepted")
	}
	s, err := coordination.OpenSharedBroker(db, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := httptest.NewTLSServer(transport.BrokerHandler(s))
	defer h.Close()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	client := []string{"--endpoint", h.URL, "--project", "project", "--token-file", token, "--ca-file", ca}
	info, err := invoke(append([]string{"shared-broker-info"}, client...)...)
	if err != nil || !strings.Contains(info, `"scope":"shared"`) {
		t.Fatalf("info: %s %v", info, err)
	}
	command := filepath.Join(dir, "command.json")
	if err := os.WriteFile(command, []byte(`{"schema_version":1,"request_id":"start","action":"state","session_id":"client","machine":"laptop","agent":"codex","state":"active","ttl_seconds":60}`), 0600); err != nil {
		t.Fatal(err)
	}
	request := append([]string{"shared-broker-request", "--file", command}, client...)
	first, err := invoke(request...)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := invoke(request...)
	if err != nil || retry != first {
		t.Fatalf("idempotent retry: %s %s %v", first, retry, err)
	}
	if err := os.WriteFile(command, []byte(`{"session_id":"client","after":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	feedJSON, err := invoke(append([]string{"shared-broker-pull", "--file", command}, client...)...)
	var feed coordination.BrokerFeed
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(feedJSON), &feed); err != nil || len(feed.Events) != 1 || feed.Sessions[0].Machine != "laptop" {
		t.Fatalf("feed: %+v %v", feed, err)
	}
	if _, err := invoke("shared-broker-revoke", "--db", db, "--id", "engineer"); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(append([]string{"shared-broker-info"}, client...)...); err == nil {
		t.Fatal("revocation ignored")
	}
}

func TestSharedBrokerProfiles(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "private", "store.db")
	s, err := coordination.OpenSharedBroker(db, true, "red", "host")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := httptest.NewTLSServer(transport.BrokerHandler(s))
	defer server.Close()
	profile := filepath.Join(dir, "red.json")
	token := profile + ".token"
	var out bytes.Buffer
	if err := run([]string{"shared-broker-member", "--db", db, "--id", "guest", "--permissions", "work", "--expires-at", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "--token-file", token}, &out, &out); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"shared-broker-info", "--endpoint", server.URL, "--project", "red", "--ca-file", ca, "--token-file", token, "--save-profile", profile}
	if err := run(args, &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := run(args, &out, &out); err == nil {
		t.Fatal("profile overwritten")
	}
	if err := run([]string{"shared-broker-info", "--profile", profile}, &out, &out); err != nil {
		t.Fatal(err)
	}
	p, err := connectionprofile.Read(profile)
	if err != nil {
		t.Fatal(err)
	}
	p.Authority = strings.Repeat("0", 48)
	wrong := filepath.Join(dir, "wrong.json")
	if err := connectionprofile.Write(wrong, p); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"shared-broker-info", "--profile", wrong, "--token-file", token}, &out, &out); err == nil {
		t.Fatal("accepted replacement authority")
	}
	raw, err := os.ReadFile(profile)
	if err != nil || bytes.Contains(raw, []byte("lworch_")) || bytes.Contains(raw, []byte(dir)) {
		t.Fatal("profile exposed private data")
	}
}
