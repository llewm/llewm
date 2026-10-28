// Package memsync syncs a project's curated history with a Git memory
// repository: export new envelopes, commit, rebase onto the remote, import what
// arrived, and push. Offline, it still exports and commits locally.
package memsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

// Outcomes recorded for a sync attempt.
const (
	OutcomeSynced    = "synced"
	OutcomeOffline   = "offline"
	OutcomeCollision = "collision"
	OutcomeError     = "error"
	// OutcomeDeferred means a session-start sync did what fit its budget and left
	// the rest (the push, or a fetch that did not finish in time) to a background
	// sync, which the caller starts.
	OutcomeDeferred = "deferred"
)

// Network limits. Only waiting on the remote is ever cut short: local Git and
// database steps always run to completion, so an interrupted sync never leaves a
// lock file, a half-finished rebase, or a rolled-back import behind.
const (
	// ProbeTimeout bounds the connection check before any Git network command,
	// so an unreachable remote costs at most this long.
	ProbeTimeout = 1500 * time.Millisecond
	// NetworkTimeout bounds each fetch or push once the remote answers. Git's
	// stall limits (see git.config) end a transfer that stops moving sooner.
	NetworkTimeout = 2 * time.Minute
	// minimumStep is the least session budget worth starting a fetch with.
	minimumStep = 500 * time.Millisecond
)

// DefaultClone returns the shared working copy for remote under the user's data
// directory, so every project using one remote shares one clone.
func DefaultClone(remote string) (string, error) {
	return config.DefaultMemoryClone(remote)
}

// Access holds how Git authenticates to the memory remote.
type Access struct {
	// Askpass is the optional fallback credential prompt program (see git.Askpass).
	Askpass string
	// SSHKey is an optional private key for SSH remotes.
	SSHKey   string
	NoPrompt bool
}

func (a Access) git(dir string) git {
	return git{Dir: dir, Askpass: a.Askpass, SSHKey: a.SSHKey, NoPrompt: a.NoPrompt}
}

// PlaintextCredentialFile is where Git's store helper keeps an unencrypted
// password for clone when the user explicitly chooses that last resort. It sits
// beside the clone, outside its working tree.
func PlaintextCredentialFile(clone string) string {
	return filepath.Clean(clone) + ".credentials"
}

// PlaintextHelper is the credential helper value for PlaintextCredentialFile.
func PlaintextHelper(clone string) string {
	return "store --file=" + shellQuote(PlaintextCredentialFile(clone))
}

// Setup is how `sync init` obtains credentials. Continuity never reads or
// stores a secret itself: Git prompts on the terminal and hands the result to a
// credential helper, which Setup configures in the memory clone only.
type Setup struct {
	// Helper is the credential helper to configure in the clone's own Git
	// config. Empty keeps whatever Git already uses.
	Helper string
	// Interactive lets Git prompt on the terminal for this setup.
	Interactive bool
	// Token, when set, is handed to the credential helper as the HTTPS password
	// before any remote access, so setup needs no prompt. Continuity keeps no copy.
	Token string
}

// approveToken gives token to the credential helper the runner uses, as the
// password for remote's host and the user named in remote.
func approveToken(ctx context.Context, runner git, remote, token string) error {
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("the token must be one non-empty line")
	}
	parsed, err := url.Parse(remote)
	if err != nil || !HTTPRemote(remote) || parsed.Host == "" {
		return errors.New("a token needs an http or https remote")
	}
	if parsed.User == nil || parsed.User.Username() == "" {
		return errors.New("a token needs the user name in the remote URL, as in https://USER@host/owner/repo")
	}
	if _, set := parsed.User.Password(); set {
		return errors.New("the remote URL must not contain a password")
	}
	input := fmt.Sprintf("protocol=%s\nhost=%s\nusername=%s\npassword=%s\n\n", strings.ToLower(parsed.Scheme), parsed.Host, parsed.User.Username(), token)
	return runner.runInput(ctx, input, "credential", "approve")
}

// Init clones remote into clone, or verifies an existing clone points at remote,
// then configures setup.Helper in the clone and, when interactive, lets Git
// prompt once so the helper stores the credential.
func Init(ctx context.Context, remote, clone string, access Access, setup Setup) error {
	if strings.TrimSpace(remote) == "" {
		return errors.New("a memory repository remote is required")
	}
	if _, err := os.Stat(filepath.Join(clone, ".git")); err == nil {
		origin, err := access.git(clone).run(ctx, "config", "--get", "remote.origin.url")
		if err != nil {
			return err
		}
		if origin != remote {
			return fmt.Errorf("%s is a clone of %s, not %s", clone, redact.String(origin), redact.String(remote))
		}
		if err := verifyRemote(ctx, access.git(clone), remote); err != nil {
			return err
		}
		if err := configureHelper(ctx, access.git(clone), setup.Helper); err != nil {
			return err
		}
		if setup.Token != "" {
			return approveToken(ctx, access.git(clone), remote, setup.Token)
		}
		if setup.Interactive {
			// A remote round trip lets Git prompt and the helper store the answer.
			runner := access.git(clone)
			runner.Interactive = true
			_, err := runner.run(ctx, "ls-remote", "--quiet", "origin", "HEAD")
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(clone), 0o700); err != nil {
		return err
	}
	runner := access.git(filepath.Dir(clone))
	// Resolve URL rewrites before the clone contacts anything.
	if resolved, err := runner.run(ctx, "ls-remote", "--get-url", remote); err != nil {
		return err
	} else if resolved != remote {
		return fmt.Errorf("Git would clone %s instead of %s (check url.*.insteadOf in your Git config); refusing to clone", redact.String(resolved), redact.String(remote))
	}
	runner.Helper, runner.Interactive = setup.Helper, setup.Interactive
	if setup.Token != "" {
		if err := approveToken(ctx, runner, remote, setup.Token); err != nil {
			return err
		}
	}
	if _, err := runner.run(ctx, "clone", "--quiet", remote, clone); err != nil {
		return err
	}
	if err := verifyRemote(ctx, access.git(clone), remote); err != nil {
		return err
	}
	return configureHelper(ctx, access.git(clone), setup.Helper)
}

// verifyRemote refuses a clone whose origin, as Git will actually contact it for
// fetch and push, differs from the configured remote: an edited clone config, a
// push URL, or a url.*.insteadOf rewrite in any Git configuration.
func verifyRemote(ctx context.Context, repo git, remote string) error {
	for _, args := range [][]string{{"remote", "get-url", "origin"}, {"remote", "get-url", "--push", "origin"}} {
		got, err := repo.run(ctx, args...)
		if err != nil {
			return err
		}
		if got != remote {
			return fmt.Errorf("the memory clone would contact %s instead of %s (check the clone's remote and any url.*.insteadOf in your Git config); refusing to sync", redact.String(got), redact.String(remote))
		}
	}
	return nil
}

// configureHelper makes helper the clone's only credential helper, in the
// clone's own Git config; nothing global or in the code repository changes.
func configureHelper(ctx context.Context, repo git, helper string) error {
	if helper == "" {
		return nil
	}
	if _, err := repo.run(ctx, "config", "--local", "--unset-all", "credential.helper"); err != nil && exitCode(err) != 5 {
		return err
	}
	// The empty entry clears helpers inherited from global and system config.
	for _, value := range []string{"", helper} {
		if _, err := repo.run(ctx, "config", "--local", "--add", "credential.helper", value); err != nil {
			return err
		}
	}
	return nil
}

// CheckAccess reports whether the remote answers without any prompt, as every
// sync from a hook requires.
func CheckAccess(ctx context.Context, clone string, access Access) error {
	access.NoPrompt = true
	_, err := access.git(clone).run(ctx, "ls-remote", "--quiet", "origin", "HEAD")
	return err
}

// HTTPRemote reports whether remote uses HTTP(S), where credential helpers apply.
func HTTPRemote(remote string) bool {
	lower := strings.ToLower(remote)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// UserHelper returns the credential helper the user's Git configuration already
// applies to remote, or "".
func UserHelper(ctx context.Context, remote string) string {
	value, _ := (git{Dir: os.TempDir()}).run(ctx, "config", "--get-urlmatch", "credential.helper", remote)
	return value
}

// LibsecretAvailable reports whether Git's libsecret credential helper is installed.
func LibsecretAvailable(ctx context.Context) bool {
	dir, err := (git{Dir: os.TempDir()}).run(ctx, "--exec-path")
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, "git-credential-libsecret"))
	return err == nil
}

// Helper returns the credential helper configured in clone's own Git config.
func Helper(ctx context.Context, clone string) string {
	values, _ := (git{Dir: clone}).run(ctx, "config", "--local", "--get-all", "credential.helper")
	lines := strings.Split(values, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Syncer syncs one project with its memory clone.
type Syncer struct {
	DB        *sql.DB
	ProjectID string
	Remote    string
	Clone     string
	// Askpass is the optional fallback credential prompt program.
	Askpass string
	// SSHKey is an optional private key for SSH remotes.
	SSHKey string
	// NoPrompt disables askpass for automatic synchronization.
	NoPrompt bool
	Now      func() time.Time
	// Leases, when set, also syncs cross-machine leases ([leases] remote):
	// each cycle mirrors the remote's lease refs and, when it may push,
	// publishes this machine's local-only grants.
	Leases  *lease.Service
	Archive *recordsync.Archive
	Relay   bool
}

// Result describes one sync attempt.
type Result struct {
	// Unchanged reports reuse of a completed, guarded sync proof.
	Unchanged       bool   `json:"unchanged,omitempty"`
	ID              string `json:"id"`
	Outcome         string `json:"outcome"`
	ExportedRecords int    `json:"exported_records"`
	ImportedRecords int    `json:"imported_records"`
	Collisions      int    `json:"collisions"`
	UnpushedCommits int    `json:"unpushed_commits"`
	ImportedHead    string `json:"imported_head,omitempty"`
	Detail          string `json:"detail"`
	// Forks counts record revisions found with several versions this cycle.
	// Each is kept as a fork (llewm fork list) and does not stop the import.
	Forks int `json:"forks"`
	// SupersessionForks counts records this cycle's import left superseded more
	// than once. Every successor is kept (llewm fork list).
	SupersessionForks int `json:"supersession_forks"`
	// DeferredRecords counts records of types this build cannot read. They are
	// skipped, and the import watermark holds so a newer build imports them.
	DeferredRecords int `json:"deferred_records"`
	// Converted reports that the explicit conversion cycle finished.
	Converted bool `json:"converted,omitempty"`
	// LeaseNotes reports cross-machine lease events that could not be
	// mirrored or published, and a lease step that failed.
	LeaseNotes      []string `json:"lease_notes,omitempty"`
	SemanticPending string   `json:"semantic_pending,omitempty"`
}

func (s Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Convert fetches and validates older observations and packs them into format 4.
// All old writers must be stopped first. It needs the remote.
func (s Syncer) Convert(ctx context.Context) (Result, error) {
	return s.run(ctx, plan{push: true, convert: true})
}

// Sync runs one full cycle under the clone's lock and records the attempt. A
// remote that cannot be reached yields OutcomeOffline with local work committed.
// Canceling ctx stops the sync between steps and cuts short a fetch or push in
// progress, but never a local step.
func (s Syncer) Sync(ctx context.Context) (Result, error) {
	return s.run(ctx, plan{push: true})
}

// RecordFailure retains an endpoint setup failure before a sync cycle could
// start, so destination status does not mistake failed setup for never run.
func (s Syncer) RecordFailure(ctx context.Context, detail string) (Result, error) {
	result := Result{Outcome: OutcomeError, Detail: redact.String(detail)}
	err := s.record(context.WithoutCancel(ctx), s.now(), &result)
	return result, err
}

// Session runs the part of a sync a session start waits for: export, commit,
// fetch, and import, so records from other machines reach the bootstrap. It
// waits on the remote only within budget and never pushes. OutcomeDeferred asks
// the caller to finish in a background sync.
func (s Syncer) Session(ctx context.Context, budget time.Duration) (Result, error) {
	s.NoPrompt = true
	return s.run(ctx, plan{deadline: time.Now().Add(budget), budget: budget})
}

// plan is what one sync attempt may do.
type plan struct {
	push bool
	// convert retires the lineage's older tracked formats before pushing.
	convert bool
	// deadline, when set, is when the attempt stops waiting on the remote.
	deadline time.Time
	budget   time.Duration
}

func (s Syncer) run(ctx context.Context, p plan) (Result, error) {
	started := s.now()
	result, err := s.cycleBlocks(ctx, p)
	if err != nil {
		result.Outcome, result.Detail = OutcomeError, redact.String(err.Error())
	}
	// A canceled sync is still recorded, so record without the caller's deadline.
	if recordErr := s.record(context.WithoutCancel(ctx), started, &result); recordErr != nil {
		return result, errors.Join(err, recordErr)
	}
	return result, err
}

// errBudget reports that a session sync ran out of budget before or during a
// network step. The cycle turns it into OutcomeDeferred.
var errBudget = errors.New("session sync budget spent")

// errNetworkTimeout is the cause of a fetch or push cut off by NetworkTimeout.
var errNetworkTimeout = fmt.Errorf("no answer within %s", NetworkTimeout)

// network returns the context for one fetch or push: the caller's context,
// limited by NetworkTimeout and by the plan's deadline.
func (p plan) network(ctx context.Context) (context.Context, context.CancelFunc, error) {
	limit, cause := NetworkTimeout, errNetworkTimeout
	if !p.deadline.IsZero() {
		remaining := time.Until(p.deadline)
		if remaining < minimumStep {
			return nil, nil, errBudget
		}
		if remaining < limit {
			limit, cause = remaining, errBudget
		}
	}
	bounded, cancel := context.WithTimeoutCause(ctx, limit, cause)
	return bounded, cancel, nil
}

// stopped reports why the caller's context ended, naming the step not started.
func stopped(ctx context.Context, step string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sync stopped before %s: %w", step, err)
	}
	return nil
}

// syncLeases mirrors the remote's lease refs and, when the plan may push,
// publishes local-only grants. A failure is reported as a note; it never fails
// the sync.
func (s Syncer) syncLeases(ctx context.Context, repo git, p plan) []string {
	bounded, cancel, err := p.network(ctx)
	if err != nil {
		return []string{"leases not synced: " + err.Error()}
	}
	defer cancel()
	refs, err := s.leaseRefs(bounded, repo, true)
	if err != nil {
		return []string{"leases not synced: " + redact.String(err.Error())}
	}
	service := *s.Leases
	service.Remote = refs
	var notes []string
	if p.push {
		notes, err = service.Reconcile(bounded, s.ProjectID)
	} else {
		notes, err = service.Refresh(bounded, s.ProjectID)
	}
	if err != nil {
		notes = append(notes, "leases not synced: "+redact.String(err.Error()))
	}
	return notes
}

func (s Syncer) fetch(ctx context.Context, repo git, p plan) error {
	bounded, cancel, err := p.network(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	_, err = repo.run(bounded, "fetch", "--quiet", "origin")
	return budgetCause(bounded, err)
}

func (s Syncer) push(ctx context.Context, repo git, branch string, p plan) error {
	bounded, cancel, err := p.network(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	_, err = repo.run(bounded, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch)
	return budgetCause(bounded, err)
}

// budgetCause returns the network step's own limit (errBudget or
// errNetworkTimeout) when that is what ended it, and err otherwise.
func budgetCause(bounded context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cause := context.Cause(bounded); errors.Is(cause, errBudget) || errors.Is(cause, errNetworkTimeout) {
		return cause
	}
	return err
}

// writeNew writes envelopes not yet present in the clone and returns the
// slash-separated clone paths it wrote.
func writeNew(clone string, envelopes []portable.EncodedEnvelope) (map[string]bool, error) {
	written := map[string]bool{}
	for _, encoded := range envelopes {
		relative := portable.MemoryRecordPath(encoded.Envelope)
		path := filepath.Join(clone, relative)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return written, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return written, err
		}
		temporary := path + ".tmp"
		if err := os.WriteFile(temporary, portable.MemoryFileBytes(encoded), 0o644); err != nil {
			return written, err
		}
		if err := os.Rename(temporary, path); err != nil {
			return written, err
		}
		written[filepath.ToSlash(relative)] = true
	}
	return written, nil
}

func commit(ctx context.Context, repo git, lineageDir string, records int, message string) error {
	if _, err := repo.run(ctx, "add", "--all", "--", lineageDir); err != nil {
		// Nothing exported yet means there is no directory to add, but a
		// staged format-1 removal still needs its commit.
		if records != 0 {
			return err
		}
	}
	if _, err := repo.run(ctx, "diff", "--cached", "--quiet"); err == nil {
		return nil
	} else if exitCode(err) != 1 {
		return err
	}
	_, err := repo.run(ctx, "commit", "--quiet", "-m", message)
	return err
}

// remoteBranch picks the branch every machine shares: the remote's only branch,
// else the local branch if the remote has it, else main or master, else local.
func remoteBranch(ctx context.Context, repo git, local string) string {
	output, err := repo.run(ctx, "for-each-ref", "--format=%(refname:lstrip=3)", "refs/remotes/origin/")
	if err != nil {
		return local
	}
	var branches []string
	for _, name := range strings.Fields(output) {
		if name != "HEAD" {
			branches = append(branches, name)
		}
	}
	if len(branches) == 1 {
		return branches[0]
	}
	for _, candidate := range []string{local, "main", "master"} {
		for _, name := range branches {
			if name == candidate {
				return name
			}
		}
	}
	return local
}

func remoteExists(ctx context.Context, repo git, branch string) bool {
	_, err := repo.run(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return err == nil
}

// upToDate reports whether HEAD is exactly the fetched remote branch.
func upToDate(ctx context.Context, repo git, branch string) bool {
	head, err := repo.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return true // no local commit yet, so nothing to push
	}
	remote, err := repo.run(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return err == nil && head == remote
}

func unpushed(ctx context.Context, repo git, branch string) int {
	rangeSpec := "HEAD"
	if remoteExists(ctx, repo, branch) {
		rangeSpec = "origin/" + branch + "..HEAD"
	}
	output, err := repo.run(ctx, "rev-list", "--count", rangeSpec)
	if err != nil {
		return 0
	}
	count, _ := strconv.Atoi(output)
	return count
}

func (s Syncer) importedHead(ctx context.Context) (string, error) {
	var head sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT imported_head FROM sync_attempts WHERE project_id=? AND remote=? AND imported_head IS NOT NULL ORDER BY started_at DESC, id DESC LIMIT 1`, s.ProjectID, redact.String(s.Remote)).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return head.String, err
}

func (s Syncer) record(ctx context.Context, started time.Time, result *Result) error {
	result.ID = activity.NewID()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sync_attempts(id,project_id,remote,started_at,finished_at,outcome,exported_records,imported_records,collisions,unpushed_commits,imported_head,detail,deferred_records,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?,1)`,
		result.ID, s.ProjectID, redact.String(s.Remote), started.Format(time.RFC3339Nano), s.now().Format(time.RFC3339Nano), result.Outcome,
		result.ExportedRecords, result.ImportedRecords, result.Collisions, result.UnpushedCommits, result.ImportedHead, result.Detail, result.DeferredRecords)
	return err
}

// Attempt is one recorded sync attempt.
type Attempt struct {
	Result
	Remote     string    `json:"remote"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// Status summarizes recent sync attempts for a project.
type Status struct {
	Configured    bool     `json:"configured"`
	Remote        string   `json:"remote,omitempty"`
	Clone         string   `json:"clone,omitempty"`
	Latest        *Attempt `json:"latest,omitempty"`
	LastSynced    *Attempt `json:"last_synced,omitempty"`
	OfflineStreak int      `json:"offline_streak"`
	// Settings names each deliberate setting that differs from the default, so
	// a changed behavior is always visible.
	Settings []string `json:"settings,omitempty"`
	// Identity is the project's portable lineage and local instance, when set.
	Identity *portable.Identity `json:"identity,omitempty"`
}

// ReadStatus reads the latest attempt, the latest successful one, and how many
// consecutive attempts since then could not reach the remote.
func ReadStatus(ctx context.Context, db *sql.DB, projectID string) (Status, error) {
	return readStatus(ctx, db, projectID, "", false)
}

// ReadRemoteStatus keeps one destination's health separate from other routes
// carrying the same project lineage.
func ReadRemoteStatus(ctx context.Context, db *sql.DB, projectID, remote string) (Status, error) {
	return readStatus(ctx, db, projectID, remote, true)
}

func readStatus(ctx context.Context, db *sql.DB, projectID, remote string, scoped bool) (Status, error) {
	var status Status
	query := `SELECT id,remote,started_at,finished_at,outcome,exported_records,imported_records,collisions,unpushed_commits,COALESCE(imported_head,''),detail,deferred_records FROM sync_attempts WHERE project_id=?`
	args := []any{projectID}
	if scoped {
		query += ` AND remote=?`
		args = append(args, redact.String(remote))
	}
	rows, err := db.QueryContext(ctx, query+` ORDER BY started_at DESC, id DESC LIMIT 64`, args...)
	if err != nil {
		return status, err
	}
	defer rows.Close()
	for rows.Next() {
		var attempt Attempt
		var started, finished string
		if err := rows.Scan(&attempt.ID, &attempt.Remote, &started, &finished, &attempt.Outcome, &attempt.ExportedRecords, &attempt.ImportedRecords, &attempt.Collisions, &attempt.UnpushedCommits, &attempt.ImportedHead, &attempt.Detail, &attempt.DeferredRecords); err != nil {
			return status, err
		}
		attempt.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		attempt.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
		if status.Latest == nil {
			copied := attempt
			status.Latest = &copied
		}
		if attempt.Outcome == OutcomeSynced {
			copied := attempt
			status.LastSynced = &copied
			break
		}
		if attempt.Outcome == OutcomeOffline {
			status.OfflineStreak++
		}
	}
	return status, rows.Err()
}

// Line renders a one-line sync summary for status and bootstrap.
func (s Status) Line(now time.Time) string {
	if !s.Configured {
		return ""
	}
	if s.Latest == nil {
		line := "Sync: configured, never run (llewm sync)"
		if len(s.Settings) > 0 {
			line += " · settings: " + strings.Join(s.Settings, ", ")
		}
		return line
	}
	var parts []string
	if s.LastSynced != nil {
		parts = append(parts, "last synced "+age(now.Sub(s.LastSynced.FinishedAt)))
	} else {
		parts = append(parts, "never synced")
	}
	if s.Latest.UnpushedCommits > 0 {
		parts = append(parts, fmt.Sprintf("%d unpushed commits", s.Latest.UnpushedCommits))
	}
	if s.Latest.DeferredRecords > 0 {
		parts = append(parts, fmt.Sprintf("%d records need a newer Continuity", s.Latest.DeferredRecords))
	}
	switch s.Latest.Outcome {
	case OutcomeOffline:
		if strings.Contains(s.Latest.Detail, "authentication unavailable:") {
			parts = append(parts, "authentication unavailable (automatic sync cannot prompt)")
		} else {
			parts = append(parts, fmt.Sprintf("remote unreachable (%d attempts)", s.OfflineStreak))
		}
	case OutcomeCollision:
		parts = append(parts, fmt.Sprintf("%d records with conflicting versions", s.Latest.Collisions))
	case OutcomeDeferred:
		parts = append(parts, s.Latest.Detail)
	case OutcomeError:
		parts = append(parts, "last attempt failed: "+s.Latest.Detail)
	}
	if len(s.Settings) > 0 {
		parts = append(parts, "settings: "+strings.Join(s.Settings, ", "))
	}
	return "Sync: " + strings.Join(parts, " · ")
}

func age(elapsed time.Duration) string {
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
}

func (s Syncer) Endpoint() string { return "git:" + recordsync.Hash(s.Remote) }
