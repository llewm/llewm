package main

import (
	"github.com/llewm/llewm/components/continuity/internal/redact"
	"net/url"
	"runtime"
	"strings"
)

// Hints are display-only argv rendered for the host shell; never executed.
func commandHint(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if runtime.GOOS == "windows" {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", "''") + "'"
		} else {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
		}
	}
	return "llewm " + strings.Join(quoted, " ")
}

func joinHint(lineage, remote string) string {
	if lineage == "" || remote == "" {
		return ""
	}
	if strings.Contains(remote, "://") {
		if u, err := url.Parse(remote); err == nil {
			if u.User != nil && (strings.EqualFold(u.Scheme, "ssh") || strings.EqualFold(u.Scheme, "git+ssh")) {
				// The SSH account selects the Git service; retain that nonsecret
				// routing identity while removing any embedded password.
				u.User = url.User(u.User.Username())
			} else {
				u.User = nil
			}
			u.RawQuery = ""
			u.Fragment = ""
			remote = u.String()
		}
	}
	if redact.String(remote) != remote {
		remote = "REMOTE_URL"
	}
	return commandHint("sync", "init", "--lineage", lineage, "--remote", remote)
}
