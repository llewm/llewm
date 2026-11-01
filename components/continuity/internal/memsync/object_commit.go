package memsync

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

const maxTransportCommitBytes = 1 << 20

type transportCommit struct {
	Tree    string
	Parents []string
	Message string
}

type transportBranch struct{ Ref, OID string }

// currentBranch resolves HEAD's symbolic chain without changing its links.
// An attached but unborn branch has an empty OID.
func (t *objectTransport) currentBranch(ctx context.Context) (transportBranch, error) {
	var branch transportBranch
	ref, err := t.repo.run(ctx, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || !strings.HasPrefix(ref, "refs/heads/") {
		return branch, fmt.Errorf("history transport requires an attached branch")
	}
	if _, err := t.repo.run(ctx, "check-ref-format", ref); err != nil {
		return branch, err
	}
	branch.Ref = ref
	oid, err := t.repo.run(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	if err != nil {
		if exitCode(err) == 1 {
			return branch, nil
		}
		return branch, err
	}
	if !validGitOID(oid) {
		return branch, fmt.Errorf("invalid history branch identity")
	}
	if _, err := t.readCommit(ctx, oid); err != nil {
		return branch, err
	}
	branch.OID = oid
	return branch, nil
}

// advanceBranch requires the caller's clone lock and completed namespace
// validation. It refuses a moved HEAD/tip or a history rewrite. The expected
// old identity is checked again by Git in the durable ref transaction.
func (t *objectTransport) advanceBranch(ctx context.Context, expected transportBranch, next string) error {
	if !validGitOID(next) {
		return fmt.Errorf("invalid next history commit")
	}
	current, err := t.currentBranch(ctx)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("history branch changed during object publication")
	}
	commit, err := t.readCommit(ctx, next)
	if err != nil {
		return err
	}
	if _, err := t.trees.read(ctx, t.repo, commit.Tree); err != nil {
		return err
	}
	if expected.OID != "" {
		if len(expected.OID) != len(next) {
			return fmt.Errorf("history object format changed")
		}
		if _, err := t.repo.run(ctx, "merge-base", "--is-ancestor", expected.OID, next); err != nil {
			return fmt.Errorf("history publication must retain the previous tip: %w", err)
		}
	}
	// The resolved branch must still be direct; do not replace a symbolic ref.
	if _, err := t.repo.run(ctx, "symbolic-ref", "--quiet", "--no-recurse", expected.Ref); err == nil || exitCode(err) != 1 {
		return fmt.Errorf("history branch became symbolic during publication")
	}
	old := expected.OID
	if old == "" {
		old = strings.Repeat("0", len(next))
	}
	repo := t.repo
	repo.Durable = true
	_, err = repo.run(ctx, "update-ref", "--no-deref", expected.Ref, next, old)
	return err
}

// mergeCommits preserves both histories instead of rebasing. Namespace policy
// must validate the selected base/local/remote trees before even a fast-forward
// is returned. Unrelated initial histories have an empty base; ambiguous merge
// bases fail closed rather than silently picking one. Ref publication is a
// separate compare-and-swap after the caller's remaining local work succeeds.
func (t *objectTransport) mergeCommits(ctx context.Context, local, remote, message string, validate func(context.Context, string, string, string) error, finish func(context.Context, string, string) (string, error)) (string, error) {
	if validate == nil || !validGitOID(remote) || (local != "" && (!validGitOID(local) || len(local) != len(remote))) {
		return "", fmt.Errorf("invalid history merge request")
	}
	r, err := t.readCommit(ctx, remote)
	if err != nil {
		return "", err
	}
	var l, base transportCommit
	baseOID := ""
	if local != "" {
		l, err = t.readCommit(ctx, local)
		if err != nil {
			return "", err
		}
		listing, err := readGitBytes(ctx, t.repo, 4096, "merge-base", "--all", local, remote)
		if err != nil && exitCode(err) != 1 {
			return "", err
		}
		if err == nil {
			bases := strings.Fields(string(listing))
			if len(bases) != 1 || !validGitOID(bases[0]) || len(bases[0]) != len(remote) {
				return "", fmt.Errorf("ambiguous or invalid history merge base")
			}
			baseOID = bases[0]
			base, err = t.readCommit(ctx, baseOID)
			if err != nil {
				return "", err
			}
		} else if len(listing) != 0 {
			return "", fmt.Errorf("invalid unrelated-history merge result")
		}
	}
	if err := validate(ctx, base.Tree, l.Tree, r.Tree); err != nil {
		return "", err
	}
	if local == "" || baseOID == local {
		return remote, nil
	}
	if baseOID == remote {
		return local, nil
	}
	tree, err := t.mergeTrees(ctx, base.Tree, l.Tree, r.Tree)
	if err != nil {
		return "", err
	}
	if finish != nil {
		tree, err = finish(ctx, l.Tree, tree)
		if err != nil {
			return "", err
		}
	}
	return t.writeCommit(ctx, tree, []string{local, remote}, message)
}

// readCommit verifies exact object bytes before using a commit's tree or
// parents. It accepts ordinary extra headers, including multiline signatures,
// without executing anything or treating a commit message as transport data.
func (t *objectTransport) readCommit(ctx context.Context, oid string) (transportCommit, error) {
	var commit transportCommit
	if !validGitOID(oid) {
		return commit, fmt.Errorf("invalid transport commit identity")
	}
	body, err := readGitBytes(ctx, t.repo, maxTransportCommitBytes, "cat-file", "commit", oid)
	if err != nil {
		return commit, err
	}
	actual, err := gitObjectIdentity("commit", body, len(oid))
	if err != nil || actual != oid {
		return commit, fmt.Errorf("Git commit content identity mismatch")
	}
	headers, message, ok := bytes.Cut(body, []byte("\n\n"))
	if !ok || bytes.ContainsRune(headers, 0) {
		return commit, fmt.Errorf("invalid transport commit headers")
	}
	author, committer := false, false
	previous := ""
	seenParents := map[string]bool{}
	for i, line := range bytes.Split(headers, []byte{'\n'}) {
		if len(line) != 0 && line[0] == ' ' {
			if previous == "" || previous == "tree" || previous == "parent" || previous == "author" || previous == "committer" {
				return commit, fmt.Errorf("invalid transport commit continuation")
			}
			continue
		}
		key, value, ok := bytes.Cut(line, []byte{' '})
		if !ok || len(key) == 0 || len(value) == 0 || (i == 0 && string(key) != "tree") {
			return commit, fmt.Errorf("invalid transport commit header")
		}
		previous = string(key)
		switch previous {
		case "tree":
			if commit.Tree != "" || !validGitOID(string(value)) || len(value) != len(oid) {
				return commit, fmt.Errorf("invalid transport commit tree")
			}
			commit.Tree = string(value)
		case "parent":
			parent := string(value)
			if !validGitOID(parent) || len(parent) != len(oid) || seenParents[parent] || len(commit.Parents) == 64 {
				return commit, fmt.Errorf("invalid or excessive transport commit parents")
			}
			seenParents[parent] = true
			commit.Parents = append(commit.Parents, parent)
		case "author":
			if author {
				return commit, fmt.Errorf("duplicate transport commit author")
			}
			author = true
		case "committer":
			if committer {
				return commit, fmt.Errorf("duplicate transport commit committer")
			}
			committer = true
		}
	}
	if commit.Tree == "" || !author || !committer {
		return commit, fmt.Errorf("incomplete transport commit")
	}
	commit.Message = string(message)
	syncwork.RecordWork(ctx, "transport_commits_read", 1)
	return commit, nil
}

// writeCommit creates a parent-preserving commit without an index, worktree,
// hooks, signing or rebase. It does not move any ref: callers validate namespace
// policy before publishing and compare-and-swap the expected branch separately.
func (t *objectTransport) writeCommit(ctx context.Context, tree string, parents []string, message string) (string, error) {
	if !validGitOID(tree) || len(parents) > 2 || len(message) > 64<<10 || strings.ContainsRune(message, 0) {
		return "", fmt.Errorf("invalid transport commit request")
	}
	if _, err := t.trees.read(ctx, t.repo, tree); err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree}
	seen := map[string]bool{}
	for _, parent := range parents {
		if len(parent) != len(tree) || seen[parent] {
			return "", fmt.Errorf("invalid transport commit parent")
		}
		if _, err := t.readCommit(ctx, parent); err != nil {
			return "", err
		}
		seen[parent] = true
		args = append(args, "-p", parent)
	}
	args = append(args, "-F", "-")
	repo := t.repo
	repo.Durable = true
	var output = boundedObjectBuffer{limit: 65}
	if err := repo.execOut(ctx, strings.NewReader(message), &output, args); err != nil {
		return "", err
	}
	oid := strings.TrimSuffix(output.String(), "\n")
	if !validGitOID(oid) || len(oid) != len(tree) || output.String() != oid+"\n" {
		return "", fmt.Errorf("invalid written transport commit identity")
	}
	written, err := t.readCommit(ctx, oid)
	if err != nil {
		return "", err
	}
	if written.Tree != tree || len(written.Parents) != len(parents) {
		return "", fmt.Errorf("written transport commit differs from requested tree or parents")
	}
	for i, parent := range parents {
		if written.Parents[i] != parent {
			return "", fmt.Errorf("written transport commit parent differs")
		}
	}
	syncwork.RecordWork(ctx, "transport_commits_written", 1)
	return oid, nil
}
