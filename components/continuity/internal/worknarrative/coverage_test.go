package worknarrative

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnnarratedCommitsSkipsNarratedOldAndOtherAuthors(t *testing.T) {
	ctx, service, projectID, root := testService(t)
	commit := func(message, author, date string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "source.md"), []byte(message+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "."}, {"commit", "-q", "--author", author, "-m", message}, {"rev-parse", "HEAD"}} {
			command := exec.Command("git", append([]string{"-C", root}, args...)...)
			command.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v %s", args, err, output)
			}
			if args[0] == "rev-parse" {
				return strings.TrimSpace(string(output))
			}
		}
		return ""
	}
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	if none, err := service.UnnarratedCommits(ctx, projectID); err != nil || len(none) != 0 {
		t.Fatalf("unborn branch: %v %v", none, err)
	}
	for _, args := range [][]string{{"config", "user.name", "Me"}, {"config", "user.email", "me@example.invalid"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, output)
		}
	}
	me, other := "Me <ME@example.invalid>", "Other <other@example.invalid>"
	commit("Old work", me, "2026-08-01T12:00:00Z")
	narratedHead := commit("Narrated work", me, "2026-09-18T12:00:00Z")
	commit("Someone else's work", other, "2026-09-19T08:00:00Z")
	missing := commit("Forgotten work", me, "2026-09-19T12:00:00Z")

	record, _, err := service.AddCommit(ctx, Record{ProjectID: projectID, Title: "Narrated", Creator: "test", Claims: []Claim{{Kind: "outcome", Text: "Did it.", Authority: "caller_asserted"}}}, narratedHead)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Append(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := service.UnnarratedCommits(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Commit != missing || got[0].Subject != "Forgotten work" || got[0].Member != "" {
		t.Fatalf("unnarrated = %+v", got)
	}
	line := UnnarratedLine(got)
	if !strings.Contains(line, "1 recent commit has no work narrative: "+missing[:7]+" Forgotten work.") {
		t.Fatalf("line = %q", line)
	}
}

func TestUnnarratedLineIsBounded(t *testing.T) {
	if UnnarratedLine(nil) != "" {
		t.Fatal("empty coverage produced a line")
	}
	var commits []UnnarratedCommit
	for i := 0; i < 8; i++ {
		commits = append(commits, UnnarratedCommit{Commit: strings.Repeat(string(rune('a'+i)), 40), Subject: strings.Repeat("é", 50)})
	}
	commits[0].Member = "api"
	line := UnnarratedLine(commits)
	if !strings.Contains(line, "8 recent commits have") || !strings.Contains(line, "api@aaaaaaa ") || !strings.Contains(line, "+3 more") || strings.Contains(line, "fffffff") {
		t.Fatalf("line = %q", line)
	}
	if !strings.Contains(line, strings.Repeat("é", 30)+"…") || strings.Contains(line, strings.Repeat("é", 31)) {
		t.Fatalf("subject not truncated on a rune boundary: %q", line)
	}
}
