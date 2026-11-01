package memsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// refs/heads, so the history-sync branch, its merges, and its pushes never see
// them.

// leaseHistory bounds how many events of a scope one read returns.
const leaseHistory = 20

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
	repo, err := openBareRepository(ctx, s.Clone, s.Remote, Access{Askpass: s.Askpass, SSHKey: s.SSHKey, NoPrompt: s.NoPrompt})
	if err != nil {
		return git{}, err
	}
	if email, _ := repo.run(ctx, "config", "user.email"); email == "" {
		// Local publication and parent-preserving merges need a committer.
		repo.Identity = []string{"-c", "user.name=Continuity", "-c", "user.email=continuity@localhost.invalid"}
	}
	return repo, nil
}

func (r leaseRefs) lock() (func(), error) {
	if r.locked {
		return func() {}, nil
	}
	file, err := lockFile(filepath.Join(r.clone, "continuity-sync.lock"))
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
	transport := &objectTransport{repo: r.repo}
	var events []lease.RemoteEvent
	for tip != "" && len(events) < leaseHistory {
		commit, err := transport.readCommit(ctx, tip)
		if err != nil {
			return nil, err
		}
		empty, err := gitObjectIdentity("tree", nil, len(tip))
		if err != nil || commit.Tree != empty || len(commit.Parents) > 1 {
			return nil, fmt.Errorf("lease commit has a nonempty tree or multiple parents")
		}
		if _, err := transport.trees.read(ctx, r.repo, empty); err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSpace(commit.Message), "\n")
		var event lease.RemoteEvent
		decoder := json.NewDecoder(strings.NewReader(lines[len(lines)-1]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return nil, fmt.Errorf("commit %s: %w", tip, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("lease commit has trailing event data")
		}
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("commit %s: %w", tip, err)
		}
		if len(events) != 0 && event.Scope() != events[0].Scope() {
			return nil, fmt.Errorf("lease commit chain changes scope")
		}
		event.Version = tip
		events = append(events, event)
		tip = ""
		if len(commit.Parents) == 1 {
			tip = commit.Parents[0]
		}
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
	format, err := r.repo.run(local, "rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	width := 40
	if format == "sha256" {
		width = 64
	} else if format != "sha1" {
		return "", fmt.Errorf("unsupported lease object format")
	}
	transport := &objectTransport{repo: r.repo}
	tree, err := transport.writeTree(local, nil, width)
	if err != nil {
		return "", err
	}
	var parents []string
	if expected != "" {
		parents = []string{expected}
	}
	message := fmt.Sprintf("llewm lease: %s token %d %s\n\n%s\n", event.Scope(), event.Token, event.Action, body)
	commit, err := transport.writeCommit(local, tree, parents, message)
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
