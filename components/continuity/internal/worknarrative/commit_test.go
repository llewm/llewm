package worknarrative

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddCommitAnchorsChangedFilesFromGit(t *testing.T) {
	ctx, service, projectID, root := testService(t)
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "gone.txt"), []byte("gone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "Initial")
	if err := os.WriteFile(filepath.Join(root, "source.md"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.md")
	git("rm", "-q", "gone.txt")
	git("commit", "-q", "-m", "Change source")
	head := git("rev-parse", "HEAD")

	record := Record{ID: "commit-narrative", ProjectID: projectID, Title: "Change source", Creator: "test", Claims: []Claim{{Kind: "outcome", Label: "What changed", Text: "Changed source.", Authority: "caller_asserted"}}, Anchors: []Anchor{{Kind: "roadmap_node", Target: "node-a", Authority: "caller_asserted"}}}
	anchored, result, err := service.AddCommit(ctx, record, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("second\n"))
	if result.Commit != head || result.ChangedPaths != 2 || result.FileAnchors != 1 || result.Deleted != 1 || anchored.Trigger != "commit" || anchored.OccurredFrom == nil || !anchored.OccurredFrom.Equal(result.CommittedAt) {
		t.Fatalf("result = %+v record = %+v", result, anchored)
	}
	if len(anchored.Anchors) != 3 || anchored.Anchors[0].Kind != "commit" || anchored.Anchors[0].Target != head || anchored.Anchors[1].Kind != "roadmap_node" || anchored.Anchors[2].Target != "source.md" || anchored.Anchors[2].ContentSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("anchors = %+v", anchored.Anchors)
	}
	if _, err = service.Append(ctx, anchored); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.AddCommit(ctx, anchored, head); err == nil {
		t.Fatal("duplicate commit anchor accepted")
	}
	for _, ref := range []string{"", "--output=x", "missing-ref"} {
		if _, _, err = service.AddCommit(ctx, record, ref); err == nil {
			t.Fatalf("bad ref %q accepted", ref)
		}
	}
}
