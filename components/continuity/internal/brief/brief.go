// Package brief assembles a compact, record-backed brief for one plan item so
// a fresh session can start a named slice without searching files.
package brief

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/standinggrant"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

// MaxBytes bounds a rendered brief.
const MaxBytes = 6 << 10

// labelPattern is a slice label written as a title prefix, such as "4e" in
// "4e: Squash-merge detection" or "5a2" in "5a2: Branch-aware selection".
var labelPattern = regexp.MustCompile(`^([0-9]+[a-z][a-z0-9]*):\s`)

// Label is the short slice reference in an item title, or "".
func Label(title string) string {
	match := labelPattern.FindStringSubmatch(title)
	if match == nil {
		return ""
	}
	return match[1]
}

// Candidate is one item of a plan's latest revision.
type Candidate struct {
	PlanID string `json:"plan_id"`
	ItemID string `json:"item_id"`
	Label  string `json:"label,omitempty"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"-"`
}

// Ref is the canonical reference, plan-qualified.
func (c Candidate) Ref() string { return c.PlanID + "/" + c.ItemID }

// candidates lists the items of every plan's latest revision.
func candidates(ctx context.Context, db *sql.DB, projectID string) ([]Candidate, error) {
	rows, err := db.QueryContext(ctx, `WITH latest AS (SELECT id,MAX(revision) revision FROM track_plan_revisions WHERE project_id=? GROUP BY id)
SELECT i.plan_id,i.id,i.title,i.status,i.detail FROM track_plan_items i JOIN latest l ON l.id=i.plan_id AND l.revision=i.plan_revision
WHERE i.project_id=? ORDER BY i.plan_id,i.position`, projectID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.PlanID, &c.ItemID, &c.Title, &c.Status, &c.Detail); err != nil {
			return nil, err
		}
		c.Label = Label(c.Title)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ErrNotFound means no plan item matches a reference.
var ErrNotFound = errors.New("no plan item matches")

// AmbiguousError lists the items a reference matches.
type AmbiguousError struct {
	Ref     string
	Matches []Candidate
}

func (e *AmbiguousError) Error() string {
	refs := make([]string, len(e.Matches))
	for i, m := range e.Matches {
		refs[i] = m.Ref()
	}
	return fmt.Sprintf("%q matches several plan items: %s; use PLAN/ITEM", e.Ref, strings.Join(refs, ", "))
}

// Resolve finds the plan item a reference names: "PLAN/ITEM", "item:ITEM",
// an item ID, or a slice label such as "4e". Several matches are resolved in
// favour of items that are not completed or superseded, else reported.
func Resolve(ctx context.Context, db *sql.DB, projectID, ref string) (Candidate, error) {
	ref = strings.TrimSpace(ref)
	all, err := candidates(ctx, db, projectID)
	if err != nil {
		return Candidate{}, err
	}
	plan, item := "", strings.TrimPrefix(ref, "item:")
	if before, after, ok := strings.Cut(item, "/"); ok {
		plan, item = before, after
	}
	var matches []Candidate
	for _, c := range all {
		if plan != "" && c.PlanID != plan {
			continue
		}
		if c.ItemID == item || (c.Label != "" && strings.EqualFold(c.Label, item)) {
			matches = append(matches, c)
		}
	}
	if len(matches) > 1 {
		var open []Candidate
		for _, m := range matches {
			if m.Status != "completed" && m.Status != "superseded" {
				open = append(open, m)
			}
		}
		if len(open) == 1 {
			return open[0], nil
		}
		return Candidate{}, &AmbiguousError{Ref: ref, Matches: matches}
	}
	if len(matches) == 0 {
		return Candidate{}, fmt.Errorf("%w %q", ErrNotFound, ref)
	}
	return matches[0], nil
}

// Dependency is one item this item depends on, with its status.
type Dependency struct {
	ItemID string `json:"item_id"`
	Label  string `json:"label,omitempty"`
	Status string `json:"status"`
}

// DecisionRef is a decision linked from the item's evidence.
type DecisionRef struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Disposition string `json:"disposition,omitempty"`
}

// ProcedureRef is a procedure linked from the item's evidence, with the
// active standing grants that cover it.
type ProcedureRef struct {
	ID             string     `json:"id"`
	Title          string     `json:"title,omitempty"`
	StandingGrants []GrantRef `json:"standing_grants,omitempty"`
}

// GrantRef is an active standing grant covering a procedure.
type GrantRef struct {
	ID      string `json:"id"`
	Grant   string `json:"grant"`
	Trigger string `json:"trigger"`
}

// Brief is everything a session needs to start one plan item.
type Brief struct {
	PlanID         string         `json:"plan_id"`
	PlanRevision   int            `json:"plan_revision"`
	RoadmapNodeID  string         `json:"roadmap_node_id"`
	TaskID         string         `json:"task_id,omitempty"`
	ItemID         string         `json:"item_id"`
	Label          string         `json:"label,omitempty"`
	Title          string         `json:"title"`
	Status         string         `json:"status"`
	Detail         string         `json:"detail"`
	DependsOn      []Dependency   `json:"depends_on"`
	Decisions      []DecisionRef  `json:"decisions"`
	StartingPoints []string       `json:"starting_points"`
	Docs           []string       `json:"docs"`
	Procedures     []ProcedureRef `json:"procedures"`
	OtherEvidence  []string       `json:"other_evidence"`
}

// Build resolves a reference and assembles its brief from plan, decision, and
// procedure records. Linked records that cannot be read are listed by ID only.
func Build(ctx context.Context, db *sql.DB, projectID, ref string) (Brief, error) {
	found, err := Resolve(ctx, db, projectID, ref)
	if err != nil {
		return Brief{}, err
	}
	plan, err := trackplan.Service{DB: db}.Plan(ctx, projectID, found.PlanID, 0)
	if err != nil {
		return Brief{}, err
	}
	if plan == nil {
		return Brief{}, fmt.Errorf("%w %q", ErrNotFound, ref)
	}
	byID := map[string]trackplan.Item{}
	for _, item := range plan.Items {
		byID[item.ID] = item
	}
	item := byID[found.ItemID]
	out := Brief{PlanID: plan.ID, PlanRevision: plan.Revision, RoadmapNodeID: plan.RoadmapNodeID, TaskID: plan.TaskID, ItemID: item.ID, Label: Label(item.Title), Title: item.Title, Status: item.Status, Detail: item.Detail,
		DependsOn: []Dependency{}, Decisions: []DecisionRef{}, StartingPoints: []string{}, Docs: []string{}, Procedures: []ProcedureRef{}, OtherEvidence: []string{}}
	for _, id := range item.DependsOn {
		dependency := Dependency{ItemID: id, Status: "unknown"}
		if other, ok := byID[id]; ok {
			dependency.Label, dependency.Status = Label(other.Title), other.Status
		}
		out.DependsOn = append(out.DependsOn, dependency)
	}
	for _, evidence := range item.EvidenceRefs {
		kind, value, _ := strings.Cut(evidence, ":")
		switch kind {
		case "decision":
			ref := DecisionRef{ID: value}
			if record, err := (decision.Service{DB: db}).Show(ctx, projectID, value, 0); err == nil {
				ref.Title, ref.Disposition = record.Title, record.Disposition
			}
			out.Decisions = append(out.Decisions, ref)
		case "code":
			out.StartingPoints = append(out.StartingPoints, value)
		case "docs":
			out.Docs = append(out.Docs, value)
		case "procedure":
			ref := ProcedureRef{ID: value}
			if record, err := (procedures.Service{DB: db}).Get(ctx, projectID, value, 0); err == nil && record != nil {
				ref.Title = record.Title
			}
			if grants, err := (standinggrant.Service{DB: db}).ForProcedure(ctx, projectID, value); err == nil {
				for _, grant := range grants {
					ref.StandingGrants = append(ref.StandingGrants, GrantRef{ID: grant.ID, Grant: grant.Ref(), Trigger: grant.Trigger})
				}
			}
			out.Procedures = append(out.Procedures, ref)
		default:
			out.OtherEvidence = append(out.OtherEvidence, evidence)
		}
	}
	return out, nil
}

// Markdown renders a brief within MaxBytes, dropping other evidence first.
func Markdown(b Brief) string {
	var out strings.Builder
	name := b.ItemID
	if b.Label != "" {
		name = b.Label + " " + b.ItemID
	}
	fmt.Fprintf(&out, "# Brief: %s [%s]\n\n%s\n\n", name, b.Status, b.Title)
	fmt.Fprintf(&out, "Plan: %s@%d · node %s", b.PlanID, b.PlanRevision, b.RoadmapNodeID)
	if b.TaskID != "" {
		fmt.Fprintf(&out, " · task %s", b.TaskID)
	}
	fmt.Fprintf(&out, "\n\n## Goal and done-when\n\n%s\n", b.Detail)
	if len(b.DependsOn) > 0 {
		out.WriteString("\n## Depends on\n\n")
		for _, d := range b.DependsOn {
			label := ""
			if d.Label != "" {
				label = d.Label + " "
			}
			fmt.Fprintf(&out, "- %s%s: %s\n", label, d.ItemID, d.Status)
		}
	}
	if len(b.Decisions) > 0 {
		out.WriteString("\n## Decisions\n\n")
		for _, d := range b.Decisions {
			fmt.Fprintf(&out, "- decision:%s", d.ID)
			if d.Title != "" {
				fmt.Fprintf(&out, " (%s): %s", d.Disposition, d.Title)
			}
			out.WriteString("\n")
		}
	}
	if len(b.StartingPoints) > 0 {
		out.WriteString("\n## Starting points\n\n")
		for _, s := range b.StartingPoints {
			fmt.Fprintf(&out, "- %s\n", s)
		}
	}
	if len(b.Procedures) > 0 {
		out.WriteString("\n## Procedures\n\n")
		for _, p := range b.Procedures {
			fmt.Fprintf(&out, "- procedure:%s", p.ID)
			if p.Title != "" {
				fmt.Fprintf(&out, ": %s", p.Title)
			}
			for _, grant := range p.StandingGrants {
				fmt.Fprintf(&out, " (standing grant %s: %s %s)", grant.ID, grant.Grant, strings.ReplaceAll(grant.Trigger, "_", " "))
			}
			out.WriteString("\n")
		}
	}
	if len(b.Docs) > 0 {
		fmt.Fprintf(&out, "\nDocs: %s\n", strings.Join(b.Docs, ", "))
	}
	body := out.String()
	if len(b.OtherEvidence) > 0 {
		evidence := fmt.Sprintf("\nEvidence: %s\n", strings.Join(b.OtherEvidence, ", "))
		if len(body)+len(evidence) <= MaxBytes {
			body += evidence
		}
	}
	if len(body) > MaxBytes {
		body = body[:MaxBytes-len("\n[truncated]\n")] + "\n[truncated]\n"
	}
	return body
}

// Mentioned lists the open plan items a text names by slice label or
// item:ID, in order of first mention, for one-line briefs at session start.
func Mentioned(ctx context.Context, db *sql.DB, projectID, text string) ([]Candidate, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	all, err := candidates(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	type hit struct {
		at int
		c  Candidate
	}
	var hits []hit
	seen := map[string]bool{}
	for _, c := range all {
		if c.Status == "completed" || c.Status == "superseded" || seen[c.Ref()] {
			continue
		}
		at := -1
		if i := strings.Index(text, "item:"+c.ItemID); i >= 0 {
			at = i
		} else if i := wordIndex(text, c.Ref()); i >= 0 {
			at = i
		} else if c.Label != "" {
			at = wordIndex(text, c.Label)
		}
		if at >= 0 {
			seen[c.Ref()] = true
			hits = append(hits, hit{at, c})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
	out := make([]Candidate, len(hits))
	for i, h := range hits {
		out[i] = h.c
	}
	return out, nil
}

// MentionedOrPending is Mentioned on the next action, falling back to the text
// of the still-unresolved checkpoint actions when the next action names no open
// plan item, so a checkpoint that rewrites the next action does not drop a
// pending choice that an earlier checkpoint carried forward.
func MentionedOrPending(ctx context.Context, db *sql.DB, projectID, nextAction string, pending []string) ([]Candidate, error) {
	items, err := Mentioned(ctx, db, projectID, nextAction)
	if err != nil || len(items) > 0 {
		return items, err
	}
	return Mentioned(ctx, db, projectID, strings.Join(pending, "\n"))
}

// wordIndex finds word as a whole token (letters, digits, and '-' delimit).
func wordIndex(text, word string) int {
	for from := 0; ; {
		i := strings.Index(text[from:], word)
		if i < 0 {
			return -1
		}
		i += from
		end := i + len(word)
		if (i == 0 || !tokenByte(text[i-1])) && (end == len(text) || !tokenByte(text[end])) {
			return i
		}
		from = i + 1
	}
}

func tokenByte(b byte) bool {
	return b == '-' || b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// Line is the one-line session-start summary of a mentioned item: its label,
// ID, status, and the first sentence of its detail.
func Line(c Candidate) string {
	name := c.ItemID
	if c.Label != "" {
		name = c.Label + " " + c.ItemID
	}
	summary := firstSentence(c.Detail, 220)
	ref := c.Label
	if ref == "" {
		ref = "item:" + c.ItemID
	}
	return fmt.Sprintf("%s [%s]: %s (llewm brief %s)", name, c.Status, summary, ref)
}

func firstSentence(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i+1]
	}
	if len(text) > max {
		text = strings.TrimSpace(text[:max-3]) + "..."
	}
	return text
}
