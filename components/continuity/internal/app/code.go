package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

// codeHeads is each present repository's HEAD commit and branch. A repository
// without a commit, or one Git cannot read, is left out rather than failing a
// checkpoint.
func (a *App) codeHeads(ctx context.Context) []checkpoint.CodeHead {
	return observeCodeHeads(ctx, a.Repositories)
}

func observeCodeHeads(ctx context.Context, repositories project.Repositories) []checkpoint.CodeHead {
	var heads []checkpoint.CodeHead
	for _, repository := range repositories.All() {
		output, err := gitenv.Command(ctx, "-C", repository.Root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}").Output()
		if err != nil {
			continue
		}
		heads = append(heads, checkpoint.CodeHead{Member: repository.Member, Commit: strings.TrimSpace(string(output)), Branch: currentBranch(ctx, repository.Root)})
	}
	return heads
}

// currentBranch is the checked-out branch, or "" for a detached HEAD or a name
// that is not safe to record.
func currentBranch(ctx context.Context, root string) string {
	output, err := gitenv.Command(ctx, "-C", root, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(output))
	if !checkpoint.ValidBranchName(name) {
		return ""
	}
	return name
}

// CodeLag is one repository whose checkout lacks the code a checkpoint was
// written on.
type CodeLag struct {
	Member string `json:"member,omitempty"`
	Commit string `json:"commit"`
	// Missing means the commit is not in this clone at all (pull first);
	// otherwise it is present but not contained in HEAD (another branch, or
	// history rewritten).
	Missing bool `json:"missing"`
	// Absent means the member's checkout is not present on this machine.
	Absent bool `json:"absent,omitempty"`
	// Landed names where a present but uncontained commit's changes already
	// are, by squash, rebase, or cherry-pick: "HEAD", or the default branch.
	Landed string `json:"landed,omitempty"`
}

// CodeBehind compares the code a checkpoint was written on with this machine's
// checkouts. It returns nothing when the checkpoint has no recorded context or
// every recorded commit is contained in the local HEAD.
func (a *App) CodeBehind(ctx context.Context, checkpointID string) ([]CodeLag, error) {
	return codeBehind(ctx, a.DB, a.Repositories, a.ProjectID, checkpointID)
}

func codeBehind(ctx context.Context, db *sql.DB, repositories project.Repositories, projectID, checkpointID string) ([]CodeLag, error) {
	recorded, err := checkpoint.GetContext(ctx, db, projectID, checkpointID)
	if err != nil || recorded == nil {
		return nil, err
	}
	present := map[string]project.Repository{}
	for _, repository := range repositories.All() {
		present[repository.Member] = repository
	}
	var lags []CodeLag
	for _, head := range recorded.CodeHeads {
		repository, ok := present[head.Member]
		if !ok {
			lags = append(lags, CodeLag{Member: head.Member, Commit: head.Commit, Missing: true, Absent: true})
			continue
		}
		if gitenv.Command(ctx, "-C", repository.Root, "cat-file", "-e", head.Commit+"^{commit}").Run() != nil {
			lags = append(lags, CodeLag{Member: head.Member, Commit: head.Commit, Missing: true})
			continue
		}
		if gitenv.Command(ctx, "-C", repository.Root, "merge-base", "--is-ancestor", head.Commit, "HEAD").Run() != nil {
			lags = append(lags, CodeLag{Member: head.Member, Commit: head.Commit, Landed: landedIn(ctx, repository.Root, head.Commit)})
		}
	}
	return lags, nil
}

// landedIn is where the changes of a commit that HEAD does not contain already
// are: "HEAD", else the default branch's name, else "". A squash-merged or
// rebased branch leaves its commits outside HEAD although its code is there.
func landedIn(ctx context.Context, root, commit string) string {
	if landed(ctx, root, commit, "HEAD") {
		return "HEAD"
	}
	name := defaultBranch(ctx, root)
	if name == "" || sameCommit(ctx, root, name, "HEAD") || !landed(ctx, root, commit, name) {
		return ""
	}
	// A default branch that contains the commit itself is the ordinary
	// off-branch case, not a squash or rebase.
	if gitenv.Command(ctx, "-C", root, "merge-base", "--is-ancestor", commit, name).Run() == nil {
		return ""
	}
	return name
}

// landed reports whether commit's changes are in target: every commit since
// their merge base has a patch-id equivalent in target (rebase or cherry-pick),
// or merging commit into target would leave target's tree unchanged (squash,
// including one that also took later commits). A merge that conflicts, or a
// Git without merge-tree --write-tree, counts as not landed.
func landed(ctx context.Context, root, commit, target string) bool {
	output, err := gitenv.Command(ctx, "-C", root, "cherry", target, commit).Output()
	if err == nil && len(output) > 0 && !strings.Contains("\n"+string(output), "\n+") {
		return true
	}
	merged, err := gitenv.Command(ctx, "-C", root, "merge-tree", "--write-tree", "--no-messages", target, commit).Output()
	if err != nil {
		return false
	}
	tree, err := gitenv.Command(ctx, "-C", root, "rev-parse", "--verify", "--quiet", target+"^{tree}").Output()
	if err != nil {
		return false
	}
	first, _, _ := strings.Cut(string(merged), "\n")
	return strings.TrimSpace(first) != "" && strings.TrimSpace(first) == strings.TrimSpace(string(tree))
}

// defaultBranch is the remote's default branch (origin/HEAD), else a local
// main or master, else "".
func defaultBranch(ctx context.Context, root string) string {
	if output, err := gitenv.Command(ctx, "-C", root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD").Output(); err == nil {
		if name := strings.TrimSpace(string(output)); name != "" {
			return name
		}
	}
	for _, name := range []string{"main", "master"} {
		if gitenv.Command(ctx, "-C", root, "rev-parse", "--verify", "--quiet", "refs/heads/"+name).Run() == nil {
			return name
		}
	}
	return ""
}

func sameCommit(ctx context.Context, root, left, right string) bool {
	a, errA := gitenv.Command(ctx, "-C", root, "rev-parse", "--verify", "--quiet", left+"^{commit}").Output()
	b, errB := gitenv.Command(ctx, "-C", root, "rev-parse", "--verify", "--quiet", right+"^{commit}").Output()
	return errA == nil && errB == nil && strings.TrimSpace(string(a)) == strings.TrimSpace(string(b))
}

// CodeBehindLine renders code lags as one bootstrap and status line, or "".
func CodeBehindLine(checkpointID string, lags []CodeLag) string {
	if len(lags) == 0 {
		return ""
	}
	var missing, diverged, landedHere []string
	landedElsewhere := map[string][]string{}
	var targets []string
	for _, lag := range lags {
		name := lag.Commit[:12]
		if lag.Member != "" {
			name = lag.Member + "@" + name
		}
		switch {
		case lag.Absent:
			missing = append(missing, name+" (checkout not present)")
		case lag.Missing:
			missing = append(missing, name)
		case lag.Landed == "HEAD":
			landedHere = append(landedHere, name)
		case lag.Landed != "":
			if _, ok := landedElsewhere[lag.Landed]; !ok {
				targets = append(targets, lag.Landed)
			}
			landedElsewhere[lag.Landed] = append(landedElsewhere[lag.Landed], name)
		default:
			diverged = append(diverged, name)
		}
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("Memory is ahead of code: checkpoint %s was written on %s, which this checkout does not have. Pull first.", checkpointID, strings.Join(missing, ", ")))
	}
	if len(diverged) > 0 {
		parts = append(parts, fmt.Sprintf("Code is behind or off the checkpoint: %s was written on %s, which this clone has but the current HEAD does not contain. Pull, or switch to the branch that has it.", checkpointID, strings.Join(diverged, ", ")))
	}
	if len(landedHere) > 0 {
		parts = append(parts, fmt.Sprintf("Code has the checkpoint's changes: %s was written on %s, which HEAD does not contain, but its changes landed here by squash or rebase merge. Nothing to pull.", checkpointID, strings.Join(landedHere, ", ")))
	}
	for _, target := range targets {
		parts = append(parts, fmt.Sprintf("Code is off the checkpoint: %s was written on %s, whose changes landed on %s by squash or rebase merge but are not in HEAD. Switch to %s.", checkpointID, strings.Join(landedElsewhere[target], ", "), target, target))
	}
	return strings.Join(parts, " ")
}
