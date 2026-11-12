package worknarrative

import (
	"context"
	"fmt"
	"strings"
)

// requiredCommitKinds are the claim kinds a commit narrative must carry, one per
// commit-policy section. A claim of the kind that says why it does not apply
// satisfies it.
var requiredCommitKinds = []string{"outcome", "intent", "verification", "limitation"}

// commitSection is one commit-policy heading and the claim kinds rendered under it.
type commitSection struct {
	heading string
	kinds   []string
}

// commitSections follow the commit policy's headings. Verification is split by
// authority below, so the rendering shows which checks the caller ran.
var commitSections = []commitSection{
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

// RequireCommitClaims checks that a commit narrative carries every claim kind the
// commit narrative needs.
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

// RecordCommit validates and anchors a canonical narrative to a commit without
// modifying the source repository.
func (s Service) RecordCommit(ctx context.Context, record Record, ref, member string) (Record, error) {
	if err := RequireCommitClaims(record.Claims); err != nil {
		return Record{}, err
	}
	anchored, _, err := s.AddCommitIn(ctx, record, ref, member)
	if err != nil {
		return Record{}, err
	}
	return s.Append(ctx, anchored)
}

// RenderCommitNarrative renders the canonical record for display: a title
// with the full hash, then one section per policy heading, in claim order.
// Sections without claims are omitted.
func RenderCommitNarrative(commit string, record Record) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s — %s\n", commit, record.Title)
	section := func(heading string, claims []Claim) {
		if len(claims) == 0 {
			return
		}
		fmt.Fprintf(&out, "\n## %s\n", heading)
		for _, claim := range claims {
			out.WriteString(renderClaim(claim))
		}
	}
	for _, s := range commitSections {
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

// renderClaim renders one claim as a list item. Text that is already a list stays
// as written; otherwise continuation lines are indented under the item.
func renderClaim(claim Claim) string {
	text := strings.TrimSpace(claim.Text)
	if strings.HasPrefix(text, "- ") || strings.HasPrefix(text, "* ") {
		return text + "\n"
	}
	return "- " + strings.ReplaceAll(text, "\n", "\n  ") + "\n"
}
