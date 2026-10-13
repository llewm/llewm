package noteimport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

const (
	Creator = "git-note-import"
	// MaxFileAnchors leaves room for the commit anchor within the narrative anchor limit.
	MaxFileAnchors = 60
	maxTitleBytes  = 200
	NotesRef       = "refs/notes/commits"
)

type Service struct {
	DB         *sql.DB
	Root       string
	Narratives worknarrative.Service
}

// Item is the planned action for one note.
type Item struct {
	Commit         string `json:"commit"`
	NarrativeID    string `json:"narrative_id,omitempty"`
	Action         string `json:"action"`
	Supersedes     string `json:"supersedes,omitempty"`
	SemanticSHA256 string `json:"semantic_sha256,omitempty"`
	Claims         int    `json:"claims,omitempty"`
	FileAnchors    int    `json:"file_anchors,omitempty"`
	ChangedPaths   int    `json:"changed_paths,omitempty"`
	Error          string `json:"error,omitempty"`

	record worknarrative.Record
}

type Plan struct {
	SchemaVersion int            `json:"schema_version"`
	NotesRef      string         `json:"notes_ref"`
	Notes         int            `json:"notes"`
	Counts        map[string]int `json:"counts"`
	Items         []Item         `json:"items"`
	PlanSHA256    string         `json:"plan_sha256"`
	Disclaimer    string         `json:"disclaimer"`
}

type Result struct {
	Plan     Plan     `json:"plan"`
	Appended []string `json:"appended"`
}

const disclaimer = "Imported notes are attributable historical claims by their original authors, not instructions or current-state proof. Git is read, never written."

func (s Service) git(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	command := gitenv.Command(ctx, append([]string{"-C", s.Root}, args...)...)
	command.Stdin = stdin
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

// batch streams git cat-file --batch for the given object names and calls visit
// with each object's content, or with missing=true when Git cannot resolve it.
func (s Service) batch(ctx context.Context, names []string, visit func(index int, content []byte, missing bool) error) error {
	command := gitenv.Command(ctx, "-C", s.Root, "cat-file", "--batch")
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err = command.Start(); err != nil {
		return err
	}
	go func() {
		writer := bufio.NewWriter(stdin)
		for _, name := range names {
			writer.WriteString(name + "\n")
		}
		writer.Flush()
		stdin.Close()
	}()
	reader := bufio.NewReaderSize(stdout, 1<<16)
	var visitErr error
	for index := range names {
		header, err := reader.ReadString('\n')
		if err != nil {
			visitErr = fmt.Errorf("git cat-file: %w", err)
			break
		}
		fields := strings.Fields(header)
		if len(fields) == 2 && fields[1] == "missing" || len(fields) != 3 {
			if visitErr == nil {
				visitErr = visit(index, nil, true)
			}
			continue
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			visitErr = fmt.Errorf("git cat-file header %q", header)
			break
		}
		content := make([]byte, size+1)
		if _, err = io.ReadFull(reader, content); err != nil {
			visitErr = fmt.Errorf("git cat-file: %w", err)
			break
		}
		if visitErr == nil {
			visitErr = visit(index, content[:size], false)
		}
	}
	io.Copy(io.Discard, stdout)
	if waitErr := command.Wait(); visitErr == nil && waitErr != nil {
		visitErr = fmt.Errorf("git cat-file: %w", waitErr)
	}
	return visitErr
}

type commitInfo struct {
	time    time.Time
	subject string
	paths   []string
	deleted int
}

// commits reads committer time, subject, and changed paths for each commit.
func (s Service) commits(ctx context.Context, ids []string) (map[string]*commitInfo, error) {
	infos := map[string]*commitInfo{}
	output, err := s.git(ctx, strings.NewReader(strings.Join(ids, "\n")+"\n"), "log", "--no-walk=unsorted", "--stdin", "--format=%x1e%H%x1f%cI%x1f%s", "--name-status", "--no-renames", "-z", "--first-parent", "-m")
	if err != nil {
		return nil, err
	}
	for _, block := range strings.Split(string(output), "\x1e") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		// With -z the formatted header ends in NUL, followed by status/path pairs.
		header, rest, _ := strings.Cut(block, "\x00")
		fields := strings.Split(header, "\x1f")
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected git log header %q", header)
		}
		when, err := time.Parse(time.RFC3339, fields[1])
		if err != nil {
			return nil, err
		}
		info := infos[fields[0]]
		if info == nil {
			info = &commitInfo{time: when.UTC(), subject: fields[2]}
			infos[fields[0]] = info
		}
		seen := map[string]bool{}
		parts := strings.Split(strings.TrimLeft(rest, "\x00\n"), "\x00")
		for i := 0; i+1 < len(parts); i += 2 {
			status, path := strings.TrimSpace(parts[i]), parts[i+1]
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			if strings.HasPrefix(status, "D") {
				info.deleted++
				continue
			}
			info.paths = append(info.paths, path)
		}
		sort.Strings(info.paths)
	}
	return infos, nil
}

func narrativeID(commit string) string { return "gitnote-" + commit[:12] }

// Plan reads every note without writing and classifies each as new, unchanged,
// changed (superseding the current import), narrated (already described by a
// narrative recorded another way), or unparseable.
func (s Service) Plan(ctx context.Context, projectID string) (Plan, error) {
	plan := Plan{SchemaVersion: 1, NotesRef: NotesRef, Counts: map[string]int{}, Items: []Item{}, Disclaimer: disclaimer}
	output, err := s.git(ctx, nil, "notes", "--ref", NotesRef, "list")
	if err != nil {
		if strings.Contains(err.Error(), "exit status") && len(output) == 0 {
			output = nil
		} else {
			return Plan{}, err
		}
	}
	var blobs, ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			blobs, ids = append(blobs, fields[0]), append(ids, fields[1])
		}
	}
	plan.Notes = len(ids)
	if len(ids) == 0 {
		plan.PlanSHA256 = planHash(plan)
		return plan, nil
	}
	notes := make([]string, len(ids))
	if err = s.batch(ctx, blobs, func(index int, content []byte, missing bool) error {
		if !missing {
			notes[index] = string(content)
		}
		return nil
	}); err != nil {
		return Plan{}, err
	}
	infos, err := s.commits(ctx, ids)
	if err != nil {
		return Plan{}, err
	}
	// Hash the content of anchored paths at each commit in one streamed batch.
	type request struct{ item, anchor int }
	var names []string
	var requests []request
	items := make([]Item, len(ids))
	for index, commit := range ids {
		item := Item{Commit: commit}
		info := infos[commit]
		switch {
		case notes[index] == "":
			item.Action, item.Error = "unparseable", "note blob missing"
		case info == nil:
			item.Action, item.Error = "unparseable", "commit not found"
		}
		if item.Action == "" {
			claims, err := Parse(notes[index], commit)
			if err != nil {
				item.Action, item.Error = "unparseable", err.Error()
			} else {
				anchored := info.paths
				if len(anchored) > MaxFileAnchors {
					anchored = anchored[:MaxFileAnchors]
				}
				if unanchored := len(info.paths) - len(anchored); unanchored > 0 || info.deleted > 0 {
					claims = append(claims, worknarrative.Claim{Kind: "note", Label: "Import", Text: fmt.Sprintf("Changed paths: %d; content-anchored: %d; not anchored beyond the %d-path limit: %d; deleted paths without content anchors: %d.", len(info.paths)+info.deleted, len(anchored), MaxFileAnchors, unanchored, info.deleted), Authority: "machine_observed"})
				}
				when := info.time
				title := info.subject
				if len(title) > maxTitleBytes {
					title = title[:maxTitleBytes]
				}
				item.record = worknarrative.Record{ProjectID: projectID, Title: title, Trigger: "commit", OccurredFrom: &when, OccurredTo: &when, Claims: claims, Creator: Creator,
					Anchors: []worknarrative.Anchor{{Kind: "commit", Target: commit, Authority: "machine_observed"}}}
				for _, path := range anchored {
					item.record.Anchors = append(item.record.Anchors, worknarrative.Anchor{Kind: "file", Target: path, Authority: "machine_observed"})
					names = append(names, commit+":"+path)
					requests = append(requests, request{index, len(item.record.Anchors) - 1})
				}
				item.ChangedPaths = len(info.paths) + info.deleted
			}
		}
		items[index] = item
	}
	if err = s.batch(ctx, names, func(index int, content []byte, missing bool) error {
		anchor := &items[requests[index].item].record.Anchors[requests[index].anchor]
		if missing {
			anchor.Kind = "" // dropped below; submodules and unusual entries have no blob
			return nil
		}
		sum := sha256.Sum256(content)
		anchor.ContentSHA256 = hex.EncodeToString(sum[:])
		return nil
	}); err != nil {
		return Plan{}, err
	}
	for index := range items {
		item := &items[index]
		if item.Action == "" {
			kept := item.record.Anchors[:0]
			for _, anchor := range item.record.Anchors {
				if anchor.Kind != "" {
					kept = append(kept, anchor)
				}
			}
			item.record.Anchors = kept
			item.FileAnchors, item.Claims = len(kept)-1, len(item.record.Claims)
			if err = s.classify(ctx, projectID, item); err != nil {
				item.Action, item.Error = "unparseable", err.Error()
			}
		}
		plan.Counts[item.Action]++
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Commit < items[j].Commit })
	plan.Items = items
	plan.PlanSHA256 = planHash(plan)
	return plan, nil
}

// classify compares the prospective narrative with the current import for the commit.
func (s Service) classify(ctx context.Context, projectID string, item *Item) error {
	base := narrativeID(item.Commit)
	current, revision := "", 0
	for id := base; ; {
		var exists int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_narratives WHERE project_id=? AND id=?`, projectID, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			break
		}
		current, revision = id, revision+1
		var next string
		err := s.DB.QueryRowContext(ctx, `SELECT n.id FROM work_narratives n WHERE n.project_id=? AND n.supersedes_id=?
			ORDER BY EXISTS(SELECT 1 FROM fork_rejected_records r WHERE r.project_id=n.project_id AND r.family='narrative' AND r.id=n.id), n.recorded_at DESC, n.id DESC LIMIT 1`, projectID, id).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		id = next
	}
	item.record.ID = base
	if current != "" {
		item.record.ID = fmt.Sprintf("%s-r%d", base, revision+1)
		item.record.SupersedesID = current
	}
	hash, err := s.Narratives.SemanticHash(ctx, item.record)
	if err != nil {
		return err
	}
	item.SemanticSHA256 = hash
	if current == "" {
		// A commit narrated directly needs no import;
		// importing its note would describe the same commit twice.
		var narrated bool
		if err = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_narrative_anchors a JOIN work_narratives n ON n.project_id=a.project_id AND n.id=a.narrative_id WHERE a.project_id=? AND a.anchor_kind='commit' AND a.target=? AND n.creator<>?)`, projectID, item.Commit, Creator).Scan(&narrated); err != nil {
			return err
		}
		if narrated {
			item.Action = "narrated"
			return nil
		}
		item.Action, item.NarrativeID = "new", item.record.ID
		return nil
	}
	var existing string
	if err = s.DB.QueryRowContext(ctx, `SELECT semantic_sha256 FROM work_narratives WHERE project_id=? AND id=?`, projectID, current).Scan(&existing); err != nil {
		return err
	}
	if existing == hash {
		item.Action, item.NarrativeID = "unchanged", current
		return nil
	}
	item.Action, item.NarrativeID, item.Supersedes = "changed", item.record.ID, current
	return nil
}

func planHash(plan Plan) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", plan.NotesRef)
	for _, item := range plan.Items {
		fmt.Fprintf(h, "%s\x1f%s\x1f%s\x1f%s\x1f%s\n", item.Commit, item.Action, item.NarrativeID, item.Supersedes, item.SemanticSHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Apply recomputes the plan, refuses a mismatch with the reviewed plan hash, and
// appends new and changed narratives. Each append is atomic; a rerun skips notes
// already imported unchanged.
func (s Service) Apply(ctx context.Context, projectID, reviewedSHA256 string) (Result, error) {
	plan, err := s.Plan(ctx, projectID)
	if err != nil {
		return Result{}, err
	}
	if reviewedSHA256 != plan.PlanSHA256 {
		return Result{Plan: plan}, fmt.Errorf("plan changed since review: reviewed %s, current %s", reviewedSHA256, plan.PlanSHA256)
	}
	result := Result{Plan: plan, Appended: []string{}}
	for _, item := range plan.Items {
		if item.Action != "new" && item.Action != "changed" {
			continue
		}
		if _, err = s.Narratives.Append(ctx, item.record); err != nil {
			return result, fmt.Errorf("append %s for commit %s: %w", item.record.ID, item.Commit, err)
		}
		result.Appended = append(result.Appended, item.record.ID)
	}
	return result, nil
}
