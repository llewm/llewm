package main

import (
	"bytes"
	"github.com/llewm/llewm/components/continuity/internal/clientperm"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

func TestInstallRequiresPersonBeforeClientOrStorageAccess(t *testing.T) {
	cmd := newRootCommand("llewm")
	cmd.SetIn(strings.NewReader(""))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"install", "--client", "codex"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("install lacked terminal guard: %v", err)
	}
	if out.Len() != 0 {
		t.Fatal("installer proceeded before terminal guard")
	}
}

func TestInstallClientIsExplicit(t *testing.T) {
	cmd := newRootCommand("llewm")
	cmd.SetArgs([]string{"install", "--dry-run"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "client") {
		t.Fatalf("installer inferred client: %v", err)
	}
}

func TestPermissionReviewReportsDeclinedAppliedAndCurrent(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	rules := clientperm.Rules{Client: clientperm.Codex, CodexTools: []string{"continuity_checkpoint"}}
	for _, test := range []struct{ input, want string }{{"no\n", "declined"}, {"yes\n", "applied"}, {"", "current"}} {
		cmd := &cobra.Command{}
		cmd.SetIn(strings.NewReader(test.input))
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := writeCodexSettings(cmd, rules); err != nil {
			t.Fatal(err)
		}
		if got := cmd.Annotations["llewm_permission_result"]; got != test.want {
			t.Fatalf("review result=%s want=%s", got, test.want)
		}
	}
}
