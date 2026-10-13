package worknarrative

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

// NotesRef is the Git notes ref commit narratives write.
const NotesRef = "refs/notes/commits"

// requiredCommitKinds are the claim kinds a commit narrative must carry, one per
// commit-policy section. A claim of the kind that says why it does not apply
// satisfies it.
var requiredCommitKinds = []string{"outcome", "intent", "verification", "limitation"}

// noteSection is one commit-policy heading and the claim kinds rendered under it.
type noteSection struct {
	heading string
	kinds   []string
}

// noteSections follow the commit policy's headings. Verification is split by
// authority below, so the note shows which checks the caller ran.
var noteSections = []noteSection{
	{"What changed", []string{"outcome"}},
	{"Interface changes", []string{"interface"}},
	{"Why", []string{"intent"}},
	{"Design decisions", []string{"decision", "constraint", "alternative"}},
	{"Verification", []string{"verification"}},
	{"Limitations and remaining work", []string{"limitation", "next_action"}},
	{"Notes", []string{"note"}},
}

// verificationHeadings name each authority in policy terms: checks the caller
// ran, checks Continuity observed, and checks inherited or reported by someone
// else and not rerun.
var verificationHeadings = []struct{ authority, heading string }{
	{"caller_asserted", "Verification (run in this session)"},
	{"machine_observed", "Verification (observed by Continuity)"},
	{"user_reported", "Verification (inherited or reported, not rerun)"},
}

// NoteResult reports what RecordCommit did to the commit's Git note.
type NoteResult struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
	// Action is added, appended (an amendment after an existing note),
	// unchanged (the note already holds this text), or skipped.
	Action string `json:"action"`
	Bytes  int    `json:"bytes"`
}

// RecordResult is a recorded narrative and, for a commit narrative, what
// happened to the commit's Git note.
type RecordResult struct {
	Record
	Note *NoteResult `json:"note,omitempty"`
}

// RequireCommitClaims checks that a commit narrative carries every claim kind the
// commit note needs.
func RequireCommitClaims(claims []Claim) error {
	have := map[string]bool{}
	for _, claim := range claims {
		have[claim.Kind] = true
	}
	var missing []string
	for _, kind := range requiredCommitKinds {
		if !have[kind] {
			missing = append(missing, kind)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("a commit narrative needs %s claims; add a claim saying why one does not apply rather than omitting it (missing: %s)", strings.Join(requiredCommitKinds, ", "), strings.Join(missing, ", "))
	}
	return nil
}

// RecordCommit anchors the record to the commit (AddCommitIn), requires the
// commit claim kinds, appends the narrative, and then, unless skipNote, writes
// the commit's Git note rendered from the claims. An existing different note is
// kept and the rendering is appended as an amendment. The narrative is canonical:
// when the note write fails, the appended record is returned with the error.
func (s Service) RecordCommit(ctx context.Context, record Record, ref, member string, skipNote bool) (Record, NoteResult, error) {
	note := NoteResult{Ref: NotesRef, Action: "skipped"}
	if err := RequireCommitClaims(record.Claims); err != nil {
		return Record{}, note, err
	}
	anchored, commit, err := s.AddCommitIn(ctx, record, ref, member)
	if err != nil {
		return Record{}, note, err
	}
	note.Commit = commit.Commit
	created, err := s.Append(ctx, anchored)
	if err != nil {
		return Record{}, note, err
	}
	if skipNote {
		return created, note, nil
	}
	repository, err := s.repository(member)
	if err != nil {
		return created, note, err
	}
	note, err = s.writeNote(ctx, repository.Root, commit.Commit, created)
	if err != nil {
		return created, note, fmt.Errorf("narrative:%s was recorded, but its Git note was not written: %w", created.ID, err)
	}
	return created, note, nil
}

func (s Service) writeNote(ctx context.Context, root, commit string, record Record) (NoteResult, error) {
	result := NoteResult{Ref: NotesRef, Commit: commit}
	body := RenderNote(commit, record)
	_, sections, _ := strings.Cut(body, "\n")
	sections = strings.TrimSpace(sections)
	existing, err := s.git(ctx, root, "notes", "--ref", NotesRef, "list", commit)
	args := []string{"notes", "--ref", NotesRef, "add", "-F", "-", commit}
	result.Action = "added"
	if err == nil && strings.TrimSpace(string(existing)) != "" {
		current, err := s.git(ctx, root, "notes", "--ref", NotesRef, "show", commit)
		if err != nil {
			return result, err
		}
		if strings.Contains(string(current), sections) {
			result.Action = "unchanged"
			return result, nil
		}
		body = "# Amendment from narrative " + record.RecordedAt.Format("2006-01-02") + "\n\n" + sections + "\n"
		args = []string{"notes", "--ref", NotesRef, "append", "-F", "-", commit}
		result.Action = "appended"
	}
	command := gitenv.Command(ctx, append([]string{"-C", root}, args...)...)
	command.Stdin = strings.NewReader(body)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return result, fmt.Errorf("git notes: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	result.Bytes = len(body)
	return result, nil
}

// RenderNote renders a commit narrative as the commit-policy Git note: a title
// with the full hash, then one section per policy heading, in claim order.
// Sections without claims are omitted.
func RenderNote(commit string, record Record) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s — %s\n", commit, record.Title)
	section := func(heading string, claims []Claim) {
		if len(claims) == 0 {
			return
		}
		fmt.Fprintf(&out, "\n## %s\n", heading)
		for _, claim := range claims {
			out.WriteString(noteItem(claim))
		}
	}
	for _, s := range noteSections {
		if s.heading == "Verification" {
			for _, v := range verificationHeadings {
				section(v.heading, claimsOf(record.Claims, s.kinds, v.authority))
			}
			continue
		}
		section(s.heading, claimsOf(record.Claims, s.kinds, ""))
	}
	return out.String()
}

func claimsOf(claims []Claim, kinds []string, authority string) []Claim {
	var out []Claim
	for _, claim := range claims {
		if authority != "" && claim.Authority != authority {
			continue
		}
		for _, kind := range kinds {
			if claim.Kind == kind {
				out = append(out, claim)
				break
			}
		}
	}
	return out
}

// noteItem renders one claim as a list item. Text that is already a list stays
// as written; otherwise continuation lines are indented under the item.
func noteItem(claim Claim) string {
	text := strings.TrimSpace(claim.Text)
	if strings.HasPrefix(text, "- ") || strings.HasPrefix(text, "* ") {
		return text + "\n"
	}
	return "- " + strings.ReplaceAll(text, "\n", "\n  ") + "\n"
}
