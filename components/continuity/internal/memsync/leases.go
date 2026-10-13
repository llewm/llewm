package memsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

// Cross-machine leases live in the memory repository as one ref per scope,
//
//	refs/continuity/leases/<lineage>/<scope kind>/<sha256 of scope ID, 32 hex>
//
// each pointing at a chain of commits (empty tree; the event as JSON in the
// message) whose tip is the scope's current state. Writes push the next commit
// with --force-with-lease naming the tip that was read, so the remote applies
// at most one of two racing writes; the loser rereads. The refs are outside
// refs/heads, so the history-sync branch, its rebase, and its pushes never see
// them.

// leaseHistory bounds how many events of a scope one read returns.
const leaseHistory = 20

// emptyTree is Git's well-known empty tree object.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

var lineageHex = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

type leaseRefs struct {
	repo   git
	remote string
	clone  string
	prefix string
	// locked means the caller already holds the clone's sync lock.
	locked bool
}

// LeaseRemote returns the cross-machine lease store for the syncer's project
// on its memory remote. Each read or write opens the clone and takes its sync
// lock, so it waits for a sync in progress.
func (s Syncer) LeaseRemote() lease.Remote { return syncerLeases{s} }

type syncerLeases struct{ syncer Syncer }

func (l syncerLeases) open(ctx context.Context) (leaseRefs, error) {
	repo, err := l.syncer.repository(context.WithoutCancel(ctx))
	if err != nil {
		return leaseRefs{}, err
	}
	return l.syncer.leaseRefs(ctx, repo, false)
}

func (l syncerLeases) Scopes(ctx context.Context) (map[string]lease.RemoteScope, error) {
	refs, err := l.open(ctx)
	if err != nil {
		return nil, err
	}
	return refs.Scopes(ctx)
}

func (l syncerLeases) Write(ctx context.Context, expected string, event lease.RemoteEvent) (string, error) {
	refs, err := l.open(ctx)
	if err != nil {
		return "", err
	}
	return refs.Write(ctx, expected, event)
}

func (s Syncer) leaseRefs(ctx context.Context, repo git, locked bool) (leaseRefs, error) {
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil {
		return leaseRefs{}, err
	}
	if identity == nil {
		return leaseRefs{}, errors.New("cross-machine leases need the project's portable identity; run `llewm sync` once first")
	}
	lineage := strings.TrimPrefix(identity.LineageID, "lineage:")
	if !lineageHex.MatchString(lineage) {
		return leaseRefs{}, fmt.Errorf("lineage %q cannot name a lease ref", identity.LineageID)
	}
	return leaseRefs{repo: repo, remote: s.Remote, clone: s.Clone, prefix: "refs/continuity/leases/" + lineage + "/", locked: locked}, nil
}

// repository opens the memory clone for Git commands: it must exist, point at
// the configured remote, and have a committer.
func (s Syncer) repository(ctx context.Context) (git, error) {
	repo := Access{Askpass: s.Askpass, SSHKey: s.SSHKey, NoPrompt: s.NoPrompt}.git(s.Clone)
	if _, err := os.Stat(filepath.Join(s.Clone, ".git")); err != nil {
		return git{}, fmt.Errorf("memory clone %s is missing; run `llewm sync init`", s.Clone)
	}
	if err := verifyRemote(ctx, repo, s.Remote); err != nil {
		return git{}, err
	}
	if email, _ := repo.run(ctx, "config", "user.email"); email == "" {
		// Commits and rebases both need a committer.
		repo.Identity = []string{"-c", "user.name=Continuity", "-c", "user.email=continuity@localhost.invalid"}
	}
	return repo, nil
}

func (r leaseRefs) lock() (func(), error) {
	if r.locked {
		return func() {}, nil
	}
	file, err := lockFile(filepath.Join(r.clone, ".git", "continuity-sync.lock"))
	if err != nil {
		return nil, err
	}
	return func() { file.Close() }, nil
}

func (r leaseRefs) ref(scopeKind, scopeID string) string {
	sum := sha256.Sum256([]byte(scopeID))
	return r.prefix + scopeKind + "/" + hex.EncodeToString(sum[:16])
}

// unreachable wraps a failed network step as lease.ErrRemoteUnreachable.
func unreachable(err error) error {
	return fmt.Errorf("%w: %s", lease.ErrRemoteUnreachable, redact.String(err.Error()))
}

func (r leaseRefs) network(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := probe(ctx, r.repo, r.remote, time.Time{}); err != nil {
		return nil, nil, unreachable(err)
	}
	bounded, cancel := context.WithTimeoutCause(ctx, NetworkTimeout, errNetworkTimeout)
	return bounded, cancel, nil
}

// Scopes fetches this lineage's lease refs and reads each scope's recent
// events.
func (r leaseRefs) Scopes(ctx context.Context) (map[string]lease.RemoteScope, error) {
	unlock, err := r.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	bounded, cancel, err := r.network(ctx)
	if err != nil {
		return nil, err
	}
	refspec := "+" + r.prefix + "*:" + r.prefix + "*"
	_, err = r.repo.run(bounded, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "origin", refspec)
	cancel()
	if err != nil {
		return nil, unreachable(budgetCause(bounded, err))
	}
	local := context.WithoutCancel(ctx)
	listing, err := r.repo.run(local, "for-each-ref", "--format=%(objectname) %(refname)", r.prefix)
	if err != nil {
		return nil, err
	}
	scopes := map[string]lease.RemoteScope{}
	for _, line := range strings.Split(listing, "\n") {
		version, ref, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		events, err := r.events(local, version)
		if err != nil {
			return nil, fmt.Errorf("read lease ref %s: %w", ref, err)
		}
		if len(events) == 0 || r.ref(events[0].ScopeKind, events[0].ScopeID) != ref {
			return nil, fmt.Errorf("lease ref %s does not hold its scope's events", ref)
		}
		scopes[events[0].Scope()] = lease.RemoteScope{Version: version, Events: events}
	}
	return scopes, nil
}

// events reads up to leaseHistory events ending at tip, newest first.
func (r leaseRefs) events(ctx context.Context, tip string) ([]lease.RemoteEvent, error) {
	output, err := r.repo.run(ctx, "log", fmt.Sprintf("--max-count=%d", leaseHistory), "--format=%x1e%H%x1f%B", tip)
	if err != nil {
		return nil, err
	}
	var events []lease.RemoteEvent
	for _, entry := range strings.Split(output, "\x1e") {
		commit, message, ok := strings.Cut(strings.TrimSpace(entry), "\x1f")
		if !ok {
			continue
		}
		lines := strings.Split(strings.TrimSpace(message), "\n")
		var event lease.RemoteEvent
		decoder := json.NewDecoder(strings.NewReader(lines[len(lines)-1]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return nil, fmt.Errorf("commit %s: %w", commit, err)
		}
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("commit %s: %w", commit, err)
		}
		event.Version = commit
		events = append(events, event)
	}
	return events, nil
}

// Write pushes event as the scope's next commit if the remote ref is still at
// expected.
func (r leaseRefs) Write(ctx context.Context, expected string, event lease.RemoteEvent) (string, error) {
	if err := event.Validate(); err != nil {
		return "", err
	}
	unlock, err := r.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	local := context.WithoutCancel(ctx)
	body, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	if _, err := r.output(local, "", "hash-object", "-w", "-t", "tree", "--stdin"); err != nil {
		return "", err
	}
	args := []string{"commit-tree", emptyTree}
	if expected != "" {
		args = append(args, "-p", expected)
	}
	message := fmt.Sprintf("llewm lease: %s token %d %s\n\n%s\n", event.Scope(), event.Token, event.Action, body)
	commit, err := r.output(local, message, append(args, "-F", "-")...)
	if err != nil {
		return "", err
	}
	ref := r.ref(event.ScopeKind, event.ScopeID)
	bounded, cancel, err := r.network(ctx)
	if err != nil {
		return "", err
	}
	var stdout bytes.Buffer
	err = r.repo.execOut(bounded, nil, &stdout, []string{"push", "--porcelain", "--no-verify", "origin", "--force-with-lease=" + ref + ":" + expected, commit + ":" + ref})
	cancel()
	if err != nil {
		detail := stdout.String() + " " + err.Error()
		switch {
		case strings.Contains(detail, "[remote rejected]"):
			return "", fmt.Errorf("the memory remote refused the lease ref %s: %s", ref, redact.String(strings.TrimSpace(detail)))
		case strings.Contains(detail, "stale info") || strings.Contains(detail, "[rejected]"):
			return "", lease.ErrRemoteMoved
		}
		return "", unreachable(budgetCause(bounded, err))
	}
	if _, err := r.repo.run(local, "update-ref", ref, commit); err != nil {
		return "", err
	}
	return commit, nil
}

// output runs Git with input on standard input and returns its output.
func (r leaseRefs) output(ctx context.Context, input string, args ...string) (string, error) {
	var stdout bytes.Buffer
	err := r.repo.execOut(ctx, strings.NewReader(input), &stdout, args)
	return strings.TrimSpace(stdout.String()), err
}
