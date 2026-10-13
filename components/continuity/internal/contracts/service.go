package contracts

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

const (
	SchemaVersion = 1
	// Disclaimer is attached to every read.
	Disclaimer = "Contracts are marked sections of repository Markdown; the files are canonical and the index only notices drift. A contract states a project shape; it does not authorize, execute, or prove anything."
)

// Drift states of a contract against the index.
const (
	Current = "current" // the section's hash is the indexed one
	Changed = "changed" // the section changed since it was indexed
	New     = "new"     // the section was never indexed
	Missing = "missing" // an indexed section is no longer found
)

// Entry is a contract with its drift state. Missing entries carry only what
// the index remembers (ID, path, title, kind, hash).
type Entry struct {
	Contract
	State string `json:"state"`
	// IndexedSHA256 is the hash last accepted, when it differs or is gone.
	IndexedSHA256 string     `json:"indexed_sha256,omitempty"`
	IndexedAt     *time.Time `json:"indexed_at,omitempty"`
}

// Report is every contract found, its drift, and parse problems.
type Report struct {
	SchemaVersion int      `json:"schema_version"`
	Contracts     []Entry  `json:"contracts"`
	Problems      []string `json:"problems"`
	Disclaimer    string   `json:"disclaimer"`
}

// Drifted returns the entries that are not current.
func (r Report) Drifted() []Entry {
	var result []Entry
	for _, entry := range r.Contracts {
		if entry.State != Current {
			result = append(result, entry)
		}
	}
	return result
}

type Service struct {
	DB   *sql.DB
	Root string
	Now  func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Scan finds every contract section in the repository's tracked Markdown
// files (and untracked ones not ignored), so a new file is found before it is
// committed.
func (s Service) Scan(ctx context.Context) ([]Contract, []string, error) {
	command := gitenv.Command(ctx, "-C", s.Root, "grep", "-l", "-I", "-F", "--untracked", Marker, "--", "*.md")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if exit, ok := err.(interface{ ExitCode() int }); ok && exit.ExitCode() == 1 {
			return nil, nil, nil // no file has a marker
		}
		return nil, nil, fmt.Errorf("find contract files: %s", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	var found []Contract
	var problems []string
	seen := map[string]string{}
	for _, path := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Root, path))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s is unreadable: %v", path, err))
			continue
		}
		contracts, fileProblems := Parse(filepath.ToSlash(path), string(data))
		problems = append(problems, fileProblems...)
		for _, contract := range contracts {
			if other, duplicate := seen[contract.ID]; duplicate {
				problems = append(problems, fmt.Sprintf("%s:%d: contract %q is also defined in %s", contract.Path, contract.Line, contract.ID, other))
				continue
			}
			seen[contract.ID] = fmt.Sprintf("%s:%d", contract.Path, contract.Line)
			found = append(found, contract)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return found, problems, nil
}

type indexed struct {
	ID, Path, Title, Kind, SHA256 string
	At                            time.Time
}

// index returns the latest index entry of each contract still indexed.
func (s Service) index(ctx context.Context, projectID string) (map[string]indexed, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT contract_id,path,title,kind,COALESCE(sha256,''),recorded_at FROM contract_index_entries e WHERE project_id=? AND NOT EXISTS (SELECT 1 FROM contract_index_entries n WHERE n.project_id=e.project_id AND n.contract_id=e.contract_id AND (n.recorded_at>e.recorded_at OR (n.recorded_at=e.recorded_at AND n.rowid>e.rowid)))`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]indexed{}
	for rows.Next() {
		var value indexed
		var at string
		if err := rows.Scan(&value.ID, &value.Path, &value.Title, &value.Kind, &value.SHA256, &at); err != nil {
			return nil, err
		}
		if value.SHA256 == "" {
			continue // removed from the index
		}
		if value.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		result[value.ID] = value
	}
	return result, rows.Err()
}

// Check scans the repository and compares each contract with the index.
func (s Service) Check(ctx context.Context, projectID string) (Report, error) {
	found, problems, err := s.Scan(ctx)
	if err != nil {
		return Report{}, err
	}
	known, err := s.index(ctx, projectID)
	if err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: SchemaVersion, Contracts: []Entry{}, Problems: append([]string{}, problems...), Disclaimer: Disclaimer}
	for _, contract := range found {
		entry := Entry{Contract: contract, State: New}
		if previous, ok := known[contract.ID]; ok {
			at := previous.At
			entry.IndexedAt = &at
			entry.State = Current
			if previous.SHA256 != contract.SHA256 {
				entry.State, entry.IndexedSHA256 = Changed, previous.SHA256
			}
			delete(known, contract.ID)
		}
		report.Contracts = append(report.Contracts, entry)
	}
	for _, previous := range known {
		at := previous.At
		report.Contracts = append(report.Contracts, Entry{Contract: Contract{ID: previous.ID, Title: previous.Title, Kind: previous.Kind, Path: previous.Path}, State: Missing, IndexedSHA256: previous.SHA256, IndexedAt: &at})
	}
	sort.SliceStable(report.Contracts, func(i, j int) bool { return report.Contracts[i].ID < report.Contracts[j].ID })
	return report, nil
}

// ErrNotFound means no contract section has the ID.
var ErrNotFound = errors.New("contract not found")

// Show returns one contract with its drift state.
func (s Service) Show(ctx context.Context, projectID, id string) (Entry, error) {
	report, err := s.Check(ctx, projectID)
	if err != nil {
		return Entry{}, err
	}
	for _, entry := range report.Contracts {
		if entry.ID == id {
			return entry, nil
		}
	}
	return Entry{}, fmt.Errorf("%w: no section is marked <!-- continuity:contract %s -->", ErrNotFound, id)
}

// Accept records the current state of every drifted contract in the index:
// new and changed sections with their hash, missing ones as removed. It
// returns the entries it recorded.
func (s Service) Accept(ctx context.Context, projectID string) ([]Entry, error) {
	report, err := s.Check(ctx, projectID)
	if err != nil {
		return nil, err
	}
	drifted := report.Drifted()
	if len(drifted) == 0 {
		return []Entry{}, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	at := s.now().Format(time.RFC3339Nano)
	for _, entry := range drifted {
		var sha any = entry.SHA256
		if entry.State == Missing {
			sha = nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO contract_index_entries(project_id,contract_id,path,title,kind,sha256,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?)`,
			projectID, entry.ID, entry.Path, entry.Title, entry.Kind, sha, at, SchemaVersion); err != nil {
			return nil, err
		}
	}
	return drifted, tx.Commit()
}

// Notice summarizes drift for the bootstrap, or "" when every contract is
// current and parses.
func Notice(report Report) string {
	counts := map[string][]string{}
	for _, entry := range report.Drifted() {
		counts[entry.State] = append(counts[entry.State], entry.ID)
	}
	var parts []string
	for _, state := range []string{Changed, Missing, New} {
		if ids := counts[state]; len(ids) > 0 {
			label := strings.Join(ids, ", ")
			if len(ids) > 3 {
				label = strings.Join(ids[:3], ", ") + fmt.Sprintf(", and %d more", len(ids)-3)
			}
			parts = append(parts, fmt.Sprintf("%d %s (%s)", len(ids), state, label))
		}
	}
	if len(report.Problems) > 0 {
		parts = append(parts, fmt.Sprintf("%d unparsable", len(report.Problems)))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("Contracts drifted since last indexed: %s. Review the sections, then accept them with `llewm contract check --accept`.", strings.Join(parts, "; "))
}
