package noteimport

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// CheckItem compares one commit's Git note with its directly recorded narratives.
type CheckItem struct {
	Commit        string   `json:"commit"`
	Subject       string   `json:"subject"`
	State         string   `json:"state"`
	Narratives    []string `json:"narratives,omitempty"`
	MissingInNote []string `json:"missing_in_note,omitempty"`
	MissingInNarr []string `json:"missing_in_narrative,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// CheckReport covers first-parent commits after Since up to HEAD.
type CheckReport struct {
	Since  string         `json:"since"`
	Head   string         `json:"head"`
	Counts map[string]int `json:"counts"`
	Items  []CheckItem    `json:"items"`
}

// comparedKinds are the note sections both records are expected to carry.
// Labels, wording, and note or amendment sections are not compared.
var comparedKinds = []string{"outcome", "intent", "decision", "verification", "limitation"}

// Check is a transition aid for projects that write both Git notes and direct
// narratives. For each commit after since, it reports whether a note and a
// directly recorded narrative (not an import) exist, and which compared claim
// kinds one has and the other lacks. It reads Git and the database only.
func (s Service) Check(ctx context.Context, projectID, since string) (CheckReport, error) {
	report := CheckReport{Since: since, Counts: map[string]int{}, Items: []CheckItem{}}
	head, err := s.git(ctx, nil, "rev-parse", "HEAD")
	if err != nil {
		return report, err
	}
	report.Head = strings.TrimSpace(string(head))
	output, err := s.git(ctx, nil, "rev-list", "--first-parent", "--reverse", "--end-of-options", since+"..HEAD")
	if err != nil {
		return report, err
	}
	commits := strings.Fields(string(output))
	notes := map[string]string{}
	if listed, err := s.git(ctx, nil, "notes", "--ref", NotesRef, "list"); err == nil {
		var blobs, ids []string
		for _, line := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
			if fields := strings.Fields(line); len(fields) == 2 {
				blobs, ids = append(blobs, fields[0]), append(ids, fields[1])
			}
		}
		if err = s.batch(ctx, blobs, func(index int, content []byte, missing bool) error {
			if !missing {
				notes[ids[index]] = string(content)
			}
			return nil
		}); err != nil {
			return report, err
		}
	}
	infos, err := s.commits(ctx, commits)
	if err != nil && len(commits) > 0 {
		return report, err
	}
	for _, commit := range commits {
		item := CheckItem{Commit: commit}
		if info := infos[commit]; info != nil {
			item.Subject = info.subject
		}
		narrativeKinds, ids, err := s.directKinds(ctx, projectID, commit)
		if err != nil {
			return report, err
		}
		item.Narratives = ids
		note, hasNote := notes[commit]
		switch {
		case !hasNote && len(ids) == 0:
			item.State = "neither"
		case !hasNote:
			item.State = "narrative_only"
		case len(ids) == 0:
			item.State = "note_only"
		default:
			claims, err := Parse(note, commit)
			if err != nil {
				item.State, item.Error = "note_unparseable", err.Error()
				break
			}
			noteKinds := map[string]bool{}
			for _, claim := range claims {
				noteKinds[claim.Kind] = true
			}
			for _, kind := range comparedKinds {
				switch {
				case noteKinds[kind] && !narrativeKinds[kind]:
					item.MissingInNarr = append(item.MissingInNarr, kind)
				case narrativeKinds[kind] && !noteKinds[kind]:
					item.MissingInNote = append(item.MissingInNote, kind)
				}
			}
			item.State = "consistent"
			if len(item.MissingInNarr)+len(item.MissingInNote) > 0 {
				item.State = "sections_differ"
			}
		}
		report.Counts[item.State]++
		report.Items = append(report.Items, item)
	}
	return report, nil
}

// directKinds returns claim kinds of current narratives anchored to the commit,
// excluding imported notes and superseded records.
func (s Service) directKinds(ctx context.Context, projectID, commit string) (map[string]bool, []string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT n.id, c.kind FROM work_narrative_anchors a
		JOIN work_narratives n ON n.project_id=a.project_id AND n.id=a.narrative_id
		JOIN work_narrative_claims c ON c.project_id=n.project_id AND c.narrative_id=n.id
		WHERE a.project_id=? AND a.anchor_kind='commit' AND a.target=? AND n.creator<>?
		AND NOT EXISTS (SELECT 1 FROM superseded_records s WHERE s.project_id=n.project_id AND s.family='narrative' AND s.id=n.id)`, projectID, commit, Creator)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	kinds, seen := map[string]bool{}, map[string]bool{}
	var ids []string
	for rows.Next() {
		var id, kind string
		if err = rows.Scan(&id, &kind); err != nil {
			return nil, nil, err
		}
		kinds[kind] = true
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return kinds, ids, rows.Err()
}

// Text renders a compact report.
func (r CheckReport) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Notes vs narratives for %s..%s: consistent=%d sections_differ=%d note_only=%d narrative_only=%d neither=%d note_unparseable=%d\n", r.Since, shortHash(r.Head), r.Counts["consistent"], r.Counts["sections_differ"], r.Counts["note_only"], r.Counts["narrative_only"], r.Counts["neither"], r.Counts["note_unparseable"])
	for _, item := range r.Items {
		detail := ""
		if len(item.MissingInNarr) > 0 {
			detail += " narrative lacks " + strings.Join(item.MissingInNarr, ",")
		}
		if len(item.MissingInNote) > 0 {
			detail += " note lacks " + strings.Join(item.MissingInNote, ",")
		}
		if item.Error != "" {
			detail += " " + item.Error
		}
		fmt.Fprintf(&b, "%s\t%-16s\t%s%s\n", shortHash(item.Commit), item.State, item.Subject, detail)
	}
	return b.String()
}

func shortHash(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
