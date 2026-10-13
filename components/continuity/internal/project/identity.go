package project

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

// Resolution outcomes.
const (
	OutcomeExisting  = "existing"
	OutcomeCreated   = "created"
	OutcomeRelocated = "relocated"
	OutcomeRebound   = "rebound"
	// OutcomeRelocationPending is a read-only lookup that recognized a moved
	// checkout but could not record the relocation.
	OutcomeRelocationPending = "relocation_pending"
)

// Resolver maps a project root to a stable project ID. Project IDs are opaque and
// permanent; the root path is only the project's current location.
type Resolver struct {
	DB *sql.DB
	// RootCommit returns the checkout's root-commit fingerprint, or "" when the
	// repository has no commits. It defaults to GitRootCommit.
	RootCommit func(context.Context, string) (string, error)
	Now        func() time.Time
}

type Resolution struct {
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
	Root         string `json:"root"`
	Outcome      string `json:"outcome"`
	PreviousRoot string `json:"previous_root,omitempty"`
}

// Candidate is a project whose repository fingerprint matches an unbound checkout.
type Candidate struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Root      string `json:"root"`
}

// RebindRequiredError refuses to guess which project an unbound checkout belongs to.
type RebindRequiredError struct {
	Root       string
	Candidates []Candidate
}

func (e *RebindRequiredError) Error() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "checkout %s is not bound to a project, but its repository matches ", e.Root)
	for i, candidate := range e.Candidates {
		if i > 0 {
			builder.WriteString(", ")
		}
		fmt.Fprintf(&builder, "%s (%s, last at %s)", candidate.ProjectID, candidate.Name, candidate.Root)
	}
	builder.WriteString("; run `llewm project rebind PROJECT_ID` if it moved, or `llewm project rebind --new` for a separate project")
	return builder.String()
}

// ErrNotRegistered reports that a read-only lookup found no project for a root.
var ErrNotRegistered = errors.New("project is not registered in this store")

// Register returns the project at root, creating it with a new opaque ID when
// absent. It never inspects Git or relocates; Ensure does that for live checkouts.
func (r Resolver) Register(ctx context.Context, root, name string) (string, error) {
	var id string
	err := r.immediate(ctx, func(tx execQueryer) error {
		var found bool
		var err error
		if id, found, err = projectAt(ctx, tx, root); err != nil {
			return err
		}
		if found {
			_, err = tx.ExecContext(ctx, `UPDATE projects SET name=? WHERE id=? AND name<>?`, name, id, name)
			return err
		}
		id, _, err = r.create(ctx, tx, root, name, &lazyCommit{done: true})
		return err
	})
	return id, err
}

// Ensure resolves the live checkout at root. A known location resolves directly.
// An unknown location is relocated only when the store lives inside the checkout
// and the repository evidence identifies exactly one project; any other match
// requires an explicit rebind. A root with no match becomes a new project.
func (r Resolver) Ensure(ctx context.Context, root, name string, storeInsideRoot bool) (Resolution, error) {
	// Hooks open the store constantly and concurrently, so a known, fingerprinted
	// checkout resolves without writing or taking the write lock.
	if resolution, ok, err := r.current(ctx, root, name); err != nil || ok {
		return resolution, err
	}
	commit := r.fingerprint(root)
	// Read Git before locking so the write lock is never held across a subprocess.
	if _, err := commit.get(ctx); err != nil {
		return Resolution{}, err
	}
	var resolution Resolution
	err := r.immediate(ctx, func(tx execQueryer) error {
		var err error
		if resolution, err = r.resolve(ctx, tx, root, commit, storeInsideRoot); err != nil {
			return err
		}
		switch resolution.Outcome {
		case OutcomeExisting:
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET name=? WHERE id=? AND name<>?`, name, resolution.ProjectID, name); err != nil {
				return err
			}
			// A symlinked spelling resolves to the recorded root, which is what is observed.
			resolution.RepositoryID, err = r.observe(ctx, tx, resolution.ProjectID, resolution.Root, commit, "observed")
		case OutcomeRelocationPending:
			resolution.Outcome = OutcomeRelocated
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET root=?, name=? WHERE id=?`, root, name, resolution.ProjectID); err != nil {
				return err
			}
			resolution.RepositoryID, err = r.observe(ctx, tx, resolution.ProjectID, root, commit, "relocated")
		default:
			resolution.Outcome = OutcomeCreated
			resolution.ProjectID, resolution.RepositoryID, err = r.create(ctx, tx, root, name, commit)
		}
		return err
	})
	return resolution, err
}

// current returns the project at root when nothing needs recording: the name is
// unchanged and the latest location observation is this path with a fingerprint.
func (r Resolver) current(ctx context.Context, root, name string) (Resolution, bool, error) {
	var resolution Resolution
	var recordedName, lastPath string
	var lastCommit sql.NullString
	err := r.DB.QueryRowContext(ctx, `SELECT p.id, p.name, r.id, l.path, l.root_commit
        FROM projects p JOIN repositories r ON r.project_id = p.id AND r.role = 'primary'
        JOIN repository_locations l ON l.id = (SELECT MAX(id) FROM repository_locations WHERE repository_id = r.id)
        WHERE p.root = ?`, root).Scan(&resolution.ProjectID, &recordedName, &resolution.RepositoryID, &lastPath, &lastCommit)
	if errors.Is(err, sql.ErrNoRows) {
		return Resolution{}, false, nil
	}
	if err != nil {
		return Resolution{}, false, err
	}
	// A workspace folder has no fingerprint to wait for.
	if recordedName != name || lastPath != root || !lastCommit.Valid && exists(filepath.Join(root, ".git")) {
		return Resolution{}, false, nil
	}
	resolution.Root, resolution.Outcome = root, OutcomeExisting
	return resolution, true, nil
}

// immediate runs fn in a BEGIN IMMEDIATE transaction. Taking the write lock up
// front avoids the SQLITE_BUSY that a deferred read-then-write transaction hits
// when another process writes first; busy_timeout covers waiting for the lock.
func (r Resolver) immediate(ctx context.Context, fn func(execQueryer) error) error {
	conn, err := r.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := fn(conn); err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	return nil
}

// Lookup resolves root like Ensure without writing. A recognized move is reported
// as OutcomeRelocationPending; an unknown root returns ErrNotRegistered.
func (r Resolver) Lookup(ctx context.Context, root string, storeInsideRoot bool) (Resolution, error) {
	commit := r.fingerprint(root)
	tx, err := r.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Resolution{}, err
	}
	defer tx.Rollback()
	resolution, err := r.resolve(ctx, tx, root, commit, storeInsideRoot)
	if err != nil {
		return Resolution{}, err
	}
	if resolution.Outcome == OutcomeCreated {
		return Resolution{}, ErrNotRegistered
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM repositories WHERE project_id=? AND role='primary'`, resolution.ProjectID).Scan(&resolution.RepositoryID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Resolution{}, err
	}
	return resolution, nil
}

// Rebind explicitly binds root to an existing project, or to a new project when
// projectID is empty. It refuses when a different project already holds root.
func (r Resolver) Rebind(ctx context.Context, root, name, projectID string) (Resolution, error) {
	commit := r.fingerprint(root)
	if _, err := commit.get(ctx); err != nil {
		return Resolution{}, err
	}
	resolution := Resolution{Root: root, Outcome: OutcomeRebound}
	err := r.immediate(ctx, func(tx execQueryer) error {
		holder, held, err := projectAt(ctx, tx, root)
		if err != nil {
			return err
		}
		if held && holder != projectID {
			return fmt.Errorf("project %s already holds %s; rebind cannot move a second project onto it", holder, root)
		}
		if projectID == "" {
			resolution.Outcome = OutcomeCreated
			resolution.ProjectID, resolution.RepositoryID, err = r.create(ctx, tx, root, name, commit)
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT root FROM projects WHERE id=?`, projectID).Scan(&resolution.PreviousRoot); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("unknown project %s", projectID)
			}
			return err
		}
		resolution.ProjectID = projectID
		source := "rebound"
		if held {
			resolution.Outcome, resolution.PreviousRoot, source = OutcomeExisting, "", "observed"
		} else if _, err := tx.ExecContext(ctx, `UPDATE projects SET root=? WHERE id=?`, root, projectID); err != nil {
			return err
		}
		resolution.RepositoryID, err = r.observe(ctx, tx, projectID, root, commit, source)
		return err
	})
	if err != nil {
		return Resolution{}, err
	}
	return resolution, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type execQueryer interface {
	queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// resolve classifies root without writing. OutcomeCreated means no project matched.
func (r Resolver) resolve(ctx context.Context, q queryer, root string, commit *lazyCommit, storeInsideRoot bool) (Resolution, error) {
	if id, found, err := projectAt(ctx, q, root); err != nil || found {
		return Resolution{ProjectID: id, Root: root, Outcome: OutcomeExisting}, err
	}
	projects, err := allProjects(ctx, q)
	if err != nil {
		return Resolution{}, err
	}
	// The same directory reached through a different spelling (symlinks) is not a move.
	for _, candidate := range projects {
		if samePath(candidate.Root, root) {
			return Resolution{ProjectID: candidate.ProjectID, Root: candidate.Root, Outcome: OutcomeExisting}, nil
		}
	}
	fingerprint, err := commit.get(ctx)
	if err != nil {
		return Resolution{}, err
	}
	var matches []Candidate
	if fingerprint != "" {
		if matches, err = projectsWithRootCommit(ctx, q, fingerprint); err != nil {
			return Resolution{}, err
		}
	}
	relocate := func(candidate Candidate) Resolution {
		return Resolution{ProjectID: candidate.ProjectID, Root: root, Outcome: OutcomeRelocationPending, PreviousRoot: candidate.Root}
	}
	if storeInsideRoot {
		// The store travels with the checkout, so its project is this checkout's.
		if len(matches) == 1 {
			return relocate(matches[0]), nil
		}
		if len(matches) == 0 && len(projects) == 1 {
			only := projects[0]
			fingerprinted, err := hasRootCommit(ctx, q, only.ProjectID)
			if err != nil {
				return Resolution{}, err
			}
			if !fingerprinted && !exists(only.Root) {
				return relocate(only), nil
			}
		}
		if len(matches) > 1 {
			return Resolution{}, &RebindRequiredError{Root: root, Candidates: matches}
		}
		if len(matches) == 0 && len(projects) > 0 {
			return Resolution{}, &RebindRequiredError{Root: root, Candidates: projects}
		}
		return Resolution{Root: root, Outcome: OutcomeCreated}, nil
	}
	if len(matches) > 0 {
		return Resolution{}, &RebindRequiredError{Root: root, Candidates: matches}
	}
	return Resolution{Root: root, Outcome: OutcomeCreated}, nil
}

func (r Resolver) create(ctx context.Context, tx execQueryer, root, name string, commit *lazyCommit) (string, string, error) {
	fingerprint, err := commit.get(ctx)
	if err != nil {
		return "", "", err
	}
	projectID, err := randomID("project-")
	if err != nil {
		return "", "", err
	}
	repositoryID, err := randomID("repo-")
	if err != nil {
		return "", "", err
	}
	now := r.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, root, name, created_at) VALUES (?, ?, ?, ?)`, projectID, root, name, now); err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repositories(id, project_id, role, created_at, schema_version) VALUES (?, ?, 'primary', ?, 1)`, repositoryID, projectID, now); err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repository_locations(project_id, repository_id, path, root_commit, source, observed_at, schema_version) VALUES (?, ?, ?, NULLIF(?, ''), 'observed', ?, 1)`, projectID, repositoryID, root, fingerprint, now); err != nil {
		return "", "", err
	}
	return projectID, repositoryID, nil
}

// observe records the primary repository's location, creating the repository if needed.
func (r Resolver) observe(ctx context.Context, tx execQueryer, projectID, root string, commit *lazyCommit, source string) (string, error) {
	var repositoryID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM repositories WHERE project_id=? AND role='primary'`, projectID).Scan(&repositoryID)
	if errors.Is(err, sql.ErrNoRows) {
		if repositoryID, err = randomID("repo-"); err != nil {
			return "", err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO repositories(id, project_id, role, created_at, schema_version) VALUES (?, ?, 'primary', ?, 1)`, repositoryID, projectID, r.now()); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return repositoryID, r.observeRepository(ctx, tx, projectID, repositoryID, root, commit, source)
}

// observeRepository appends a location observation when the path or fingerprint changed.
func (r Resolver) observeRepository(ctx context.Context, tx execQueryer, projectID, repositoryID, root string, commit *lazyCommit, source string) error {
	var lastPath string
	var lastCommit sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT path, root_commit FROM repository_locations WHERE repository_id=? ORDER BY id DESC LIMIT 1`, repositoryID).Scan(&lastPath, &lastCommit)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// A fingerprinted location needs no Git call; hooks open the store constantly.
	if err == nil && lastPath == root && lastCommit.Valid {
		return nil
	}
	fingerprint, err := commit.get(ctx)
	if err != nil {
		return err
	}
	if lastPath == root && fingerprint == "" && !lastCommit.Valid {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO repository_locations(project_id, repository_id, path, root_commit, source, observed_at, schema_version) VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, 1)`, projectID, repositoryID, root, fingerprint, source, r.now())
	return err
}

func projectAt(ctx context.Context, q queryer, root string) (string, bool, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT id FROM projects WHERE root=?`, root).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

func allProjects(ctx context.Context, q queryer) ([]Candidate, error) {
	return candidates(q.QueryContext(ctx, `SELECT id, name, root FROM projects ORDER BY id`))
}

func projectsWithRootCommit(ctx context.Context, q queryer, commit string) ([]Candidate, error) {
	return candidates(q.QueryContext(ctx, `SELECT p.id, p.name, p.root FROM projects p
        WHERE EXISTS (SELECT 1 FROM repository_locations l JOIN repositories r ON r.id = l.repository_id AND r.role = 'primary'
            WHERE l.project_id = p.id AND l.root_commit = ?)
        ORDER BY p.id`, commit))
}

func hasRootCommit(ctx context.Context, q queryer, projectID string) (bool, error) {
	var found int
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM repository_locations l JOIN repositories r ON r.id = l.repository_id AND r.role = 'primary'
        WHERE l.project_id=? AND l.root_commit IS NOT NULL)`, projectID).Scan(&found)
	return found == 1, err
}

func candidates(rows *sql.Rows, err error) ([]Candidate, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Candidate
	for rows.Next() {
		var candidate Candidate
		if err := rows.Scan(&candidate.ProjectID, &candidate.Name, &candidate.Root); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

// lazyCommit computes a checkout's fingerprint at most once, and only when needed.
type lazyCommit struct {
	read  func(context.Context, string) (string, error)
	root  string
	done  bool
	value string
	err   error
}

func (r Resolver) fingerprint(root string) *lazyCommit {
	read := r.RootCommit
	if read == nil {
		read = GitRootCommit
	}
	return &lazyCommit{read: read, root: root}
}

func (c *lazyCommit) get(ctx context.Context) (string, error) {
	if !c.done {
		c.value, c.err = c.read(ctx, c.root)
		c.done = true
	}
	return c.value, c.err
}

func (r Resolver) now() string {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	return now().UTC().Format(time.RFC3339Nano)
}

// GitRootCommit returns the lexically first root commit reachable from HEAD, or ""
// for a repository without commits.
func GitRootCommit(ctx context.Context, root string) (string, error) {
	// A workspace folder is not a repository even when an enclosing directory is.
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", nil
	}
	command := gitenv.Command(ctx, "-C", root, "rev-list", "--max-parents=0", "HEAD", "--")
	output, err := command.Output()
	if err != nil {
		verify := gitenv.Command(ctx, "-C", root, "rev-parse", "--verify", "--quiet", "HEAD")
		if verify.Run() != nil {
			return "", nil
		}
		return "", fmt.Errorf("read root commit of %s: %w", root, err)
	}
	roots := strings.Fields(string(output))
	if len(roots) == 0 {
		return "", nil
	}
	sort.Strings(roots)
	return roots[0], nil
}

func randomID(prefix string) (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func samePath(a, b string) bool {
	left, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	right, err := filepath.EvalSymlinks(b)
	return err == nil && left == right
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Location is one append-only observation of where a repository's checkout lives.
type Location struct {
	RepositoryID string `json:"repository_id"`
	Path         string `json:"path"`
	RootCommit   string `json:"root_commit,omitempty"`
	Source       string `json:"source"`
	ObservedAt   string `json:"observed_at"`
}

// Locations returns the newest observations of the project root's location, newest first.
func Locations(ctx context.Context, q queryer, projectID string, limit int) ([]Location, error) {
	rows, err := q.QueryContext(ctx, `SELECT repository_id, path, COALESCE(root_commit, ''), source, observed_at
        FROM repository_locations WHERE project_id=? AND repository_id IN (SELECT id FROM repositories WHERE project_id=? AND role='primary')
        ORDER BY id DESC LIMIT ?`, projectID, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Location{}
	for rows.Next() {
		var location Location
		if err := rows.Scan(&location.RepositoryID, &location.Path, &location.RootCommit, &location.Source, &location.ObservedAt); err != nil {
			return nil, err
		}
		result = append(result, location)
	}
	return result, rows.Err()
}

// MemberState is one workspace member as recorded in the store.
type MemberState struct {
	Path         string `json:"path"`
	RepositoryID string `json:"repository_id,omitempty"`
	Location     string `json:"location"`
	RootCommit   string `json:"root_commit,omitempty"`
	Present      bool   `json:"present"`
}

// Members reports each manifest member with its recorded repository, if any.
func Members(ctx context.Context, q queryer, projectID string, workspace Workspace) ([]MemberState, error) {
	recorded, err := recordedMembers(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	result := make([]MemberState, 0, len(workspace.Members))
	for _, member := range workspace.Members {
		state := recorded[member.Path]
		state.Path, state.Location = member.Path, workspace.MemberRoot(member)
		state.Present = exists(filepath.Join(state.Location, ".git"))
		result = append(result, state)
	}
	return result, nil
}

func recordedMembers(ctx context.Context, q queryer, projectID string) (map[string]MemberState, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.member_path, r.id, COALESCE(l.path, ''), COALESCE(l.root_commit, '')
        FROM repositories r LEFT JOIN repository_locations l
            ON l.id = (SELECT MAX(id) FROM repository_locations WHERE repository_id = r.id)
        WHERE r.project_id = ? AND r.role = 'member'`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]MemberState{}
	for rows.Next() {
		var path string
		var state MemberState
		var location string
		if err := rows.Scan(&path, &state.RepositoryID, &location, &state.RootCommit); err != nil {
			return nil, err
		}
		state.Location = location
		result[path] = state
	}
	return result, rows.Err()
}

// SyncMembers records each present manifest member as a member repository and
// observes its location and fingerprint. It writes nothing when every present
// member is already recorded, fingerprinted, at its current location.
func (r Resolver) SyncMembers(ctx context.Context, projectID string, workspace Workspace) error {
	recorded, err := recordedMembers(ctx, r.DB, projectID)
	if err != nil {
		return err
	}
	type pending struct {
		member Member
		root   string
		commit *lazyCommit
	}
	var changes []pending
	for _, member := range workspace.Members {
		root := workspace.MemberRoot(member)
		if !exists(filepath.Join(root, ".git")) {
			continue
		}
		state, ok := recorded[member.Path]
		if ok && state.Location == root && state.RootCommit != "" {
			continue
		}
		commit := r.fingerprint(root)
		// Read Git before locking so the write lock is never held across a subprocess.
		if _, err := commit.get(ctx); err != nil {
			return err
		}
		changes = append(changes, pending{member, root, commit})
	}
	if len(changes) == 0 {
		return nil
	}
	return r.immediate(ctx, func(tx execQueryer) error {
		for _, change := range changes {
			var repositoryID string
			err := tx.QueryRowContext(ctx, `SELECT id FROM repositories WHERE project_id=? AND role='member' AND member_path=?`, projectID, change.member.Path).Scan(&repositoryID)
			if errors.Is(err, sql.ErrNoRows) {
				if repositoryID, err = randomID("repo-"); err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO repositories(id, project_id, role, member_path, created_at, schema_version) VALUES (?, ?, 'member', ?, ?, 1)`, repositoryID, projectID, change.member.Path, r.now())
			}
			if err != nil {
				return err
			}
			if err := r.observeRepository(ctx, tx, projectID, repositoryID, change.root, change.commit, "observed"); err != nil {
				return err
			}
		}
		return nil
	})
}
