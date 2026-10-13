// Package noteimport converts sectioned Git commit notes into work narratives with
// one typed claim per section. It reads Git and never writes to it.
package noteimport

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

var (
	headingPattern = regexp.MustCompile(`^(#{1,2})\s+(.+?)\s*#*\s*$`)
	labelPattern   = regexp.MustCompile(`^(?:[-*]\s+)?\*{0,2}([A-Za-z][A-Za-z ,/()'-]{0,60}?)\*{0,2}:\s*(.*)$`)
	commitPattern  = regexp.MustCompile("^(?:[-*]\\s+)?Commit:\\s*`?([0-9a-f]{7,40})`?\\s*$")
	hashPattern    = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
)

// KindFor maps a section heading to a claim kind. Order matters: verification and
// limitation headings often also mention changes, and "What changed and why" is an
// outcome section.
func KindFor(label string) string {
	l := strings.ToLower(label)
	has := func(words ...string) bool {
		for _, word := range words {
			if strings.Contains(l, word) {
				return true
			}
		}
		return false
	}
	switch {
	case has("failed attempt", "alternative", "rejected"):
		return "alternative"
	case has("verification", "qualification", "verified"):
		return "verification"
	case has("limitation", "limits", "remaining", "next work", "next step", "known issue"):
		return "limitation"
	case has("design", "decision"):
		return "decision"
	case has("interface"):
		return "interface"
	case has("what changed"):
		return "outcome"
	case has("why", "rationale", "motivation"):
		return "intent"
	case has("changed", "changes", "summary"):
		return "outcome"
	}
	return "note"
}

// titleLine reports whether a level-one heading is a note title such as
// "# Commit <hash>" rather than a section.
func titleLine(level int, label string) bool {
	if level != 1 {
		return false
	}
	l := strings.ToLower(label)
	return hashPattern.MatchString(label) || strings.HasPrefix(l, "commit") || strings.Contains(l, "commit note") || strings.Contains(l, "commit record")
}

type section struct {
	label string
	lines []string
}

// Parse splits a note into claims. Markdown headings (# and ##) define sections.
// Outside a heading section, "- Label: text" bullets and bare "Label:" lines whose
// label maps to a known kind also start sections. Text before the first section
// becomes an unlabeled outcome. A title or "Commit:" hash that does not match the
// commit is an error.
func Parse(note, commit string) ([]worknarrative.Claim, error) {
	lines := strings.Split(strings.ReplaceAll(note, "\r\n", "\n"), "\n")
	sections := []section{{}}
	seenContent, inHeading := false, false
	checkHash := func(value string) error {
		for _, hash := range hashPattern.FindAllString(value, -1) {
			if !strings.HasPrefix(commit, hash) {
				return fmt.Errorf("title hash %s does not match commit %s", hash, commit)
			}
		}
		return nil
	}
	for _, line := range lines {
		first := !seenContent
		if strings.TrimSpace(line) != "" {
			seenContent = true
		}
		if match := headingPattern.FindStringSubmatch(line); match != nil {
			level, label := len(match[1]), strings.Trim(match[2], "`* ")
			// A leading level-one heading is the note title (often the commit
			// subject) unless it names a known section such as "# Why".
			if titleLine(level, label) || first && level == 1 && KindFor(label) == "note" {
				if err := checkHash(label); err != nil {
					return nil, err
				}
				continue
			}
			sections, inHeading = append(sections, section{label: label}), true
			continue
		}
		if match := commitPattern.FindStringSubmatch(line); match != nil {
			if err := checkHash(match[1]); err != nil {
				return nil, err
			}
			continue
		}
		if !inHeading {
			if match := labelPattern.FindStringSubmatch(line); match != nil && KindFor(match[1]) != "note" {
				sections = append(sections, section{label: strings.TrimSpace(match[1]), lines: []string{match[2]}})
				continue
			}
		}
		current := &sections[len(sections)-1]
		current.lines = append(current.lines, line)
	}
	var claims []worknarrative.Claim
	for _, s := range sections {
		text := strings.TrimSpace(strings.Join(s.lines, "\n"))
		if text == "" {
			continue
		}
		kind := "outcome"
		if s.label != "" {
			kind = KindFor(s.label)
		}
		label := s.label
		if len(label) > worknarrative.MaxClaimLabelBytes {
			label = label[:worknarrative.MaxClaimLabelBytes]
		}
		claims = append(claims, worknarrative.Claim{Kind: kind, Label: label, Text: text, Authority: "caller_asserted"})
	}
	if len(claims) == 0 {
		return nil, fmt.Errorf("note has no content")
	}
	if len(claims) > worknarrative.MaxClaims {
		return nil, fmt.Errorf("note has %d sections; at most %d are supported", len(claims), worknarrative.MaxClaims)
	}
	return claims, nil
}
