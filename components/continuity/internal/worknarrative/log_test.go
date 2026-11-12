package worknarrative

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
)

func TestLogNarrativeAttribution(t *testing.T) {
	ctx, service, projectID, root := testService(t)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("init", "-q")
	git("config", "user.name", "Git Author")
	git("config", "user.email", "author@example.invalid")
	git("add", ".")
	git("commit", "-qm", "Test commit")
	log := func(oneline bool) string {
		t.Helper()
		out, err := service.Log(ctx, projectID, LogOptions{Limit: 1, Oneline: oneline})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := log(false); !strings.Contains(out, "Narrative agents: unknown (no current narrative)") {
		t.Fatal(out)
	}
	add := func(id, creator, invocation, supersedes string) {
		t.Helper()
		_, err := service.RecordCommit(ctx, Record{ID: id, ProjectID: projectID, Title: id, Creator: creator, CreatorInvocationID: invocation, SupersedesID: supersedes, Claims: commitClaims("Tested")}, "HEAD", "")
		if err != nil {
			t.Fatal(err)
		}
	}
	add("legacy", "old-agent", "", "")
	if out := log(false); !strings.Contains(out, `creator="old-agent" agent="unknown" provider="unknown" model="unknown"`) || !strings.Contains(out, `Invocation: "unknown"`) {
		t.Fatal(out)
	}
	for _, value := range []attribution.Invocation{
		{ID: "codex-invocation", ProjectID: projectID, SessionID: "codex-session", ActorType: "agent", Client: "codex", Provider: "openai", Model: "model-a", IdentitySource: "client_hook_claim"},
		{ID: "claude-invocation", ProjectID: projectID, SessionID: "claude-session", ActorType: "agent", Client: "claude-code", Provider: "anthropic", Model: "model-b", IdentitySource: "caller_asserted"},
	} {
		if _, err := (attribution.Service{DB: service.DB}).AppendInvocation(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	add("codex-change", "codex", "codex-invocation", "narrative:legacy")
	add("claude-change", "claude", "claude-invocation", "")
	add("codex-review", "codex", "codex-invocation", "")
	out := log(false)
	for _, want := range []string{"Author: Git Author <author@example.invalid>", `creator="codex" agent="codex" provider="openai" model="model-a"`, `creator="claude" agent="claude-code" provider="anthropic" model="model-b"`, "Narrative: codex-change", "Narrative: claude-change", `Creator: "codex" (caller asserted)`, `Invocation: "codex-invocation"; session: "codex-session"; identity source: "client_hook_claim"`, `Invocation: "claude-invocation"; session: "claude-session"; identity source: "caller_asserted"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "old-agent") {
		t.Fatalf("superseded creator included: %s", out)
	}
	compact := log(true)
	if strings.Count(compact, `agent="codex"`) != 1 || !strings.Contains(compact, `model="model-b"`) || strings.Contains(compact, "Narratives:") || strings.Count(compact, "\n") != 1 {
		t.Fatalf("compact: %s", compact)
	}
}
