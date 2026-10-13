package memsync

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestProbeAddress(t *testing.T) {
	for remote, want := range map[string]string{
		"https://user@git.example.com/owner/repo": "git.example.com:443",
		"http://git.example.com/owner/repo":       "git.example.com:80",
		"https://git.example.com:8443/repo":       "git.example.com:8443",
		"https://[::1]:3000/repo":                 "[::1]:3000",
		"ssh://git@git.example.com/repo":          "",
		"git@git.example.com:repo.git":            "",
		"/srv/memory.git":                         "",
	} {
		if got := probeAddress(remote); got != want {
			t.Errorf("probeAddress(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestProbeReportsAnUnreachableRemoteQuickly(t *testing.T) {
	for _, name := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"} {
		t.Setenv(name, "")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := "http://" + listener.Addr().String() + "/memory"
	repo := git{Dir: t.TempDir()}
	if err := probe(context.Background(), repo, open, time.Time{}); err != nil {
		t.Fatalf("listening remote: %v", err)
	}
	listener.Close()
	started := time.Now()
	if err := probe(context.Background(), repo, open, time.Time{}); err == nil || !strings.Contains(err.Error(), "cannot connect") {
		t.Fatalf("closed remote: %v", err)
	}
	if elapsed := time.Since(started); elapsed > ProbeTimeout+time.Second {
		t.Fatalf("probe took %s", elapsed)
	}
	if err := probe(context.Background(), repo, open, time.Now()); err != errBudget {
		t.Fatalf("spent budget: %v", err)
	}
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	if err := probe(context.Background(), repo, open, time.Time{}); err != nil {
		t.Fatalf("proxied remote was probed directly: %v", err)
	}
}
