package memsync

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/app"
)

func TestConversionRetainsRejectedRemoteHeadAndResolution(t *testing.T) {
	t.Parallel()
	remote := bareRemote(t)
	a := newMachine(t, "old-writer", remote)
	b := newMachine(t, "converter", remote)
	a.topic(t, "transport", "Old remote alternative")
	plantFormerFormat(t, a)
	b.topic(t, "transport", "Accepted local alternative")
	result, err := b.syncer.Convert(context.Background())
	if err != nil || !result.Converted || result.Forks != 1 {
		t.Fatalf("conversion fork: %+v %v", result, err)
	}
	forks := b.forks(t)
	if len(forks) != 1 || len(forks[0].Heads) != 2 {
		t.Fatalf("lost alternative: %+v", forks)
	}
	var accepted, rejected string
	for _, head := range forks[0].Heads {
		if head.Local {
			accepted = head.ContentSHA256
		} else {
			rejected = head.ContentSHA256
		}
	}
	resolver := &app.App{DB: b.db, ProjectID: b.project}
	resolution, err := resolver.ResolveRevisionFork(context.Background(), "topic_revision:transport@1", accepted, "owner", "user_reported")
	if err != nil || len(resolution.Resolution.RejectedHeads) != 1 || resolution.Resolution.RejectedHeads[0] != rejected {
		t.Fatalf("lost rejection: %+v %v", resolution, err)
	}
	b.sync(t, OutcomeSynced)
	c := newMachine(t, "fresh", remote)
	c.sync(t, OutcomeSynced)
	if got := c.topicTitle(t, "transport", 2); got != "Accepted local alternative" || len(c.forks(t)) != 0 {
		t.Fatalf("resolution reopened on fresh machine: %q %+v", got, c.forks(t))
	}
	var count int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM fork_head_observations WHERE project_id=? AND content_sha256=?`, c.project, rejected).Scan(&count); err != nil || count == 0 {
		t.Fatalf("rejected observation lost: %d %v", count, err)
	}
	if again, err := b.syncer.Convert(context.Background()); err != nil || again.ExportedRecords != 0 || again.Forks != 0 {
		t.Fatalf("conversion reopened settled history: %+v %v", again, err)
	}
}

func TestCorruptPreviousObservationRefusesConversionWithoutRemovalOrPush(t *testing.T) {
	t.Parallel()
	remote := bareRemote(t)
	a := newMachine(t, "old-writer", remote)
	b := newMachine(t, "converter", remote)
	a.decide(t, "remote", "Keep remote evidence")
	plantFormerFormat(t, a)
	files, err := exec.Command("git", "-C", a.syncer.Clone, "ls-tree", "-r", "--name-only", "HEAD", "--", "v2").Output()
	if err != nil {
		t.Fatal(err)
	}
	relative := strings.TrimSpace(string(files))
	repo, err := a.syncer.repository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixtureCommitFiles(t, repo, map[string][]byte{relative: []byte("invalid observation\n")}, nil, "corrupt fixture")
	if _, err := repo.run(context.Background(), "push", "--quiet", "origin", "HEAD"); err != nil {
		t.Fatal(err)
	}
	before, err := exec.Command("git", "-C", remote, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := b.syncer.Convert(context.Background()); err == nil || result.Converted {
		t.Fatalf("corrupt conversion succeeded: %+v %v", result, err)
	}
	peer, err := b.syncer.repository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	branch := remoteBranch(context.Background(), peer, "master")
	if body, err := peer.run(context.Background(), "show", "refs/remotes/origin/"+branch+":"+relative); err != nil || body != "invalid observation" {
		t.Fatalf("fetched evidence removed: %q %v", body, err)
	}
	after, err := exec.Command("git", "-C", remote, "rev-parse", "HEAD").Output()
	if err != nil || string(after) != string(before) {
		t.Fatalf("remote changed after refusal: %s %s %v", before, after, err)
	}
}
