package memsync

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// probe opens and closes a TCP connection to an HTTP(S) remote's host within
// ProbeTimeout, so an unreachable remote is reported in about a second instead
// of after Git's own, much longer, connect timeout. It returns errBudget when
// deadline leaves no time for it. SSH remotes rely on ssh's ConnectTimeout
// instead (ssh configuration may rename the host or add a proxy), and local
// remotes and remotes behind a proxy are not probed.
func probe(ctx context.Context, repo git, remote string, deadline time.Time) error {
	limit := ProbeTimeout
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining < minimumStep {
			return errBudget
		}
		limit = min(limit, remaining)
	}
	address := probeAddress(remote)
	if address == "" || proxied(ctx, repo, remote) {
		return nil
	}
	dialer := net.Dialer{Timeout: limit}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("cannot connect to %s: %w", address, err)
	}
	return connection.Close()
}

// probeAddress is host:port for an HTTP(S) remote, or "" when it is not probed.
func probeAddress(remote string) string {
	if !HTTPRemote(remote) {
		return ""
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
		if strings.EqualFold(parsed.Scheme, "http") {
			port = "80"
		}
	}
	return net.JoinHostPort(parsed.Hostname(), port)
}

// proxied reports whether Git would reach remote through a proxy, where a
// direct connection says nothing about reachability.
func proxied(ctx context.Context, repo git, remote string) bool {
	for _, name := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"} {
		if os.Getenv(name) != "" {
			return true
		}
	}
	value, _ := repo.run(ctx, "config", "--get-urlmatch", "http.proxy", remote)
	return value != ""
}
