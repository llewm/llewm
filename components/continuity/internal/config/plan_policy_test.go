package config

import (
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"os"
	"path/filepath"
	"testing"
)

func TestGlobalProjectAndTrackPolicyPrecedence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, GlobalFileName), []byte("[plan_policy]\nmode='agent'\n[plan_policy.tracks.track]\nmode='bounded'\nallow=['dependencies']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPlanPolicy(root, trackplan.Policy{Mode: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if p.For("track").Mode != "review" || p.For("other").Mode != "review" {
		t.Fatal("global track bypassed project restriction", p)
	}
	p, err = LoadPlanPolicy(root, trackplan.Policy{Mode: "review", Tracks: map[string]trackplan.Rule{"track": {Mode: "bounded", Allow: []string{"add_items"}}}})
	if err != nil || p.For("track").Mode != "bounded" || p.For("track").Allow[0] != "add_items" {
		t.Fatal(p, err)
	}
	if err := os.WriteFile(filepath.Join(root, GlobalFileName), []byte("[plan_policy]\nmode='typo'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPlanPolicy(root, trackplan.Policy{}); err == nil {
		t.Fatal("bad policy accepted")
	}
	if _, err := LoadPlanPolicy(root, trackplan.Policy{Mode: "review"}); err == nil {
		t.Fatal("invalid global policy hidden by project override")
	}
}
