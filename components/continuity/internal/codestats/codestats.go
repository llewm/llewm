// Package codestats counts lines added and removed per commit from Git's
// numstat output, without a model: totals and breakdowns by language,
// extension, day, author, commit, and by the task, plan item, or roadmap node
// Continuity records tie a commit to.
package codestats

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

// Change is one file's line counts in one commit. Binary files have no counts.
type Change struct {
	Commit  string
	Subject string
	Author  string
	At      time.Time
	Path    string
	Added   int
	Removed int
	Binary  bool
}

// Options selects commits and groups the report.
type Options struct {
	By            string
	Since, Until  time.Time
	Range         string
	IncludeMerges bool
	// Exclude adds path patterns to DefaultExclude: a pattern ending in / is a
	// directory anywhere in the path; others match the whole path or the base
	// name with path.Match.
	Exclude []string
	Limit   int
}

// DefaultExclude skips generated, vendored, and lock files.
var DefaultExclude = []string{"vendor/", "node_modules/", "go.sum", "*.lock", "package-lock.json", "pnpm-lock.yaml", "*.min.js", "*.pb.go"}

var groupings = []string{"language", "extension", "day", "author", "commit", "directory", "task", "plan-item", "node"}

// Row is one group's totals. Commits and files count distinct commits and
// paths.
type Row struct {
	Key     string `json:"key"`
	Commits int    `json:"commits"`
	Files   int    `json:"files"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// Report is the result. With a record grouping (task, plan-item, node), a
// commit tied to several keys counts toward each, and Unattributed holds the
// commits tied to none.
type Report struct {
	By           string    `json:"by"`
	Since        time.Time `json:"since,omitzero"`
	Until        time.Time `json:"until,omitzero"`
	Range        string    `json:"range,omitempty"`
	Total        Row       `json:"total"`
	Excluded     Row       `json:"excluded"`
	Unattributed *Row      `json:"unattributed,omitempty"`
	Rows         []Row     `json:"rows"`
	Truncated    bool      `json:"truncated,omitempty"`
}

// Collect reads the numstat log of every repository. Paths in a workspace
// member carry the member prefix.
func Collect(ctx context.Context, repositories []project.Repository, options Options) ([]Change, error) {
	if strings.HasPrefix(options.Range, "-") {
		return nil, fmt.Errorf("range %q must be a revision range, not an option", options.Range)
	}
	var changes []Change
	for _, repository := range repositories {
		args := []string{"-C", repository.Root, "log", "--numstat", "--no-renames", "--format=%x00%H%x09%aI%x09%an%x09%s"}
		if !options.IncludeMerges {
			args = append(args, "--no-merges")
		}
		if !options.Since.IsZero() {
			args = append(args, "--since="+options.Since.Format(time.RFC3339))
		}
		if !options.Until.IsZero() {
			args = append(args, "--until="+options.Until.Format(time.RFC3339))
		}
		if options.Range != "" {
			args = append(args, options.Range)
		}
		args = append(args, "--")
		var stderr bytes.Buffer
		command := gitenv.Command(ctx, args...)
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("git log in %s: %v: %s", repository.Root, err, strings.TrimSpace(stderr.String()))
		}
		parsed, err := Parse(output, repository.Prefix())
		if err != nil {
			return nil, err
		}
		changes = append(changes, parsed...)
	}
	return changes, nil
}

// Parse reads `git log --numstat --format=%x00%H%x09%aI%x09%an%x09%s` output.
func Parse(output []byte, prefix string) ([]Change, error) {
	var changes []Change
	var current Change
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if header, ok := strings.CutPrefix(line, "\x00"); ok {
			fields := strings.SplitN(header, "\t", 4)
			if len(fields) != 4 {
				return nil, fmt.Errorf("unexpected git log header %q", header)
			}
			at, err := time.Parse(time.RFC3339, fields[1])
			if err != nil {
				return nil, fmt.Errorf("commit %s date: %w", fields[0], err)
			}
			current = Change{Commit: fields[0], At: at, Author: fields[2], Subject: fields[3]}
			continue
		}
		if current.Commit == "" {
			return nil, errors.New("numstat line before any commit header")
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected numstat line %q", line)
		}
		change := current
		change.Path = prefix + fields[2]
		if fields[0] == "-" || fields[1] == "-" {
			change.Binary = true
		} else {
			var err error
			if change.Added, err = strconv.Atoi(fields[0]); err != nil {
				return nil, fmt.Errorf("numstat line %q: %w", line, err)
			}
			if change.Removed, err = strconv.Atoi(fields[1]); err != nil {
				return nil, fmt.Errorf("numstat line %q: %w", line, err)
			}
		}
		changes = append(changes, change)
	}
	return changes, scanner.Err()
}

// Excluded reports whether a path matches one of the patterns.
func Excluded(filePath string, patterns []string) bool {
	for _, pattern := range patterns {
		if dir, ok := strings.CutSuffix(pattern, "/"); ok {
			if strings.HasPrefix(filePath, dir+"/") || strings.Contains(filePath, "/"+dir+"/") {
				return true
			}
			continue
		}
		if matched, _ := path.Match(pattern, filePath); matched {
			return true
		}
		if matched, _ := path.Match(pattern, path.Base(filePath)); matched {
			return true
		}
	}
	return false
}

var languages = map[string]string{
	".go": "Go", ".md": "Markdown", ".sql": "SQL", ".sh": "Shell", ".bash": "Shell", ".ps1": "PowerShell",
	".py": "Python", ".js": "JavaScript", ".mjs": "JavaScript", ".ts": "TypeScript", ".tsx": "TypeScript",
	".json": "JSON", ".toml": "TOML", ".yaml": "YAML", ".yml": "YAML", ".html": "HTML", ".css": "CSS",
	".rs": "Rust", ".c": "C", ".h": "C", ".cpp": "C++", ".java": "Java", ".kt": "Kotlin", ".swift": "Swift",
	".rb": "Ruby", ".txt": "Text",
}

var languageNames = map[string]string{"Makefile": "Make", "Dockerfile": "Docker", "go.mod": "Go modules"}

// Language names a file's language from its name or extension, or returns
// the extension (or "other") when it has none it knows.
func Language(filePath string) string {
	base := path.Base(filePath)
	if name, ok := languageNames[base]; ok {
		return name
	}
	extension := strings.ToLower(path.Ext(base))
	if name, ok := languages[extension]; ok {
		return name
	}
	if extension == "" {
		return "other"
	}
	return extension
}

func extension(filePath string) string {
	if value := strings.ToLower(path.Ext(path.Base(filePath))); value != "" {
		return value
	}
	return "(none)"
}

// Build groups changes into a report. attribution maps a full commit hash to
// its record keys; it is required for the task, plan-item, and node groupings.
func Build(changes []Change, options Options, attribution map[string][]string) (Report, error) {
	by := options.By
	if by == "" {
		by = "language"
	}
	known := false
	for _, value := range groupings {
		known = known || value == by
	}
	if !known {
		return Report{}, fmt.Errorf("group by %s, not %q", strings.Join(groupings, ", "), by)
	}
	records := by == "task" || by == "plan-item" || by == "node"
	exclude := append(append([]string{}, DefaultExclude...), options.Exclude...)
	report := Report{By: by, Since: options.Since, Until: options.Until, Range: options.Range}
	total, excluded, unattributed := newAccumulator(), newAccumulator(), newAccumulator()
	groups := map[string]*accumulator{}
	for _, change := range changes {
		if Excluded(change.Path, exclude) {
			excluded.add(change)
			continue
		}
		total.add(change)
		var keys []string
		switch by {
		case "language":
			keys = []string{Language(change.Path)}
		case "extension":
			keys = []string{extension(change.Path)}
		case "day":
			keys = []string{change.At.Format("2006-01-02")}
		case "author":
			keys = []string{change.Author}
		case "commit":
			keys = []string{change.Commit[:min(12, len(change.Commit))] + " " + change.Subject}
		case "directory":
			keys = []string{directoryOf(change.Path)}
		default:
			keys = attribution[change.Commit]
			if len(keys) == 0 {
				unattributed.add(change)
			}
		}
		for _, key := range keys {
			if groups[key] == nil {
				groups[key] = newAccumulator()
			}
			groups[key].add(change)
		}
	}
	report.Total, report.Excluded = total.row(""), excluded.row("")
	if records {
		row := unattributed.row("")
		report.Unattributed = &row
	}
	report.Rows = make([]Row, 0, len(groups))
	for key, group := range groups {
		report.Rows = append(report.Rows, group.row(key))
	}
	sort.Slice(report.Rows, func(i, j int) bool {
		a, b := report.Rows[i], report.Rows[j]
		if by == "day" {
			return a.Key > b.Key
		}
		if a.Added+a.Removed != b.Added+b.Removed {
			return a.Added+a.Removed > b.Added+b.Removed
		}
		return a.Key < b.Key
	})
	if options.Limit > 0 && len(report.Rows) > options.Limit {
		report.Rows, report.Truncated = report.Rows[:options.Limit], true
	}
	return report, nil
}

// directoryOf names a path's first two directories, such as
// components/continuity, or "." for a top-level file.
func directoryOf(filePath string) string {
	parts := strings.Split(path.Dir(filePath), "/")
	if parts[0] == "." {
		return "."
	}
	return strings.Join(parts[:min(2, len(parts))], "/")
}

type accumulator struct {
	commits map[string]bool
	files   map[string]bool
	added   int
	removed int
}

func newAccumulator() *accumulator {
	return &accumulator{commits: map[string]bool{}, files: map[string]bool{}}
}

func (a *accumulator) add(change Change) {
	a.commits[change.Commit] = true
	a.files[change.Path] = true
	a.added += change.Added
	a.removed += change.Removed
}

func (a *accumulator) row(key string) Row {
	return Row{Key: key, Commits: len(a.commits), Files: len(a.files), Added: a.added, Removed: a.removed}
}
