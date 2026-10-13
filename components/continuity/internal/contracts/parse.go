// Package contracts indexes project shapes (axioms, directives, promises,
// constraints) written as marked sections of Markdown files in the
// repository. The files are canonical: people and agents edit them like any
// other document, and clients that read them see the statements without
// Continuity. Continuity finds the sections, returns one exactly, hashes each,
// and reports drift against the hashes it last indexed, so a changed or
// vanished statement is noticed. Decision records keep the why; the marked
// section is the what.
package contracts

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Kinds are the project shapes a contract can state.
var Kinds = []string{"axiom", "directive", "promise", "constraint"}

// Marker starts a contract section.
const Marker = "<!-- continuity:contract"

var (
	markerPattern = regexp.MustCompile(`^<!--\s*continuity:contract\s+([A-Za-z0-9][A-Za-z0-9._-]*)\s*-->\s*$`)
	anyMarker     = regexp.MustCompile(`^<!--\s*continuity:[a-z-]+\b`)
	headingPat    = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	headerPattern = regexp.MustCompile(`^(kind|scope|expiry|review):\s*(.*?)\s*$`)
)

// Contract is one marked section as it is in its file now.
type Contract struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Kind   string `json:"kind"`
	Scope  string `json:"scope,omitempty"`
	Expiry string `json:"expiry,omitempty"`
	Review string `json:"review,omitempty"`
	// Statement is the section body after the header lines, exactly as
	// written (trimmed).
	Statement string `json:"statement"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	// SHA256 hashes the whole section from its heading to its end, so any
	// change to the title, header, or statement is drift.
	SHA256 string `json:"sha256"`
}

func oneOf(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// Parse reads the contract sections of one Markdown file. A section starts at
// `<!-- continuity:contract ID -->`, takes the next heading as its title, then
// `kind:`, `scope:`, `expiry:`, and `review:` lines (kind is required), and
// runs to the next continuity marker or the next heading of the same or a
// higher level. Markers and headings inside fenced code blocks are ignored.
// Problems name sections that cannot be indexed; the others are
// still returned.
func Parse(path, text string) ([]Contract, []string) {
	var (
		result   []Contract
		problems []string
		current  *Contract
		level    int
		header   bool
		section  []string
		body     []string
	)
	flush := func() {
		if current == nil {
			return
		}
		current.Statement = strings.TrimSpace(strings.Join(body, "\n"))
		sum := sha256.Sum256([]byte(strings.TrimSpace(strings.Join(section, "\n"))))
		current.SHA256 = hex.EncodeToString(sum[:])
		switch {
		case current.Title == "":
			problems = append(problems, fmt.Sprintf("%s:%d: contract %q has no heading", path, current.Line, current.ID))
		case !oneOf(current.Kind, Kinds):
			problems = append(problems, fmt.Sprintf("%s:%d: contract %q needs a kind: line (axiom, directive, promise, or constraint)", path, current.Line, current.ID))
		case current.Statement == "":
			problems = append(problems, fmt.Sprintf("%s:%d: contract %q has no statement", path, current.Line, current.ID))
		default:
			result = append(result, *current)
		}
		current, section, body = nil, nil, nil
	}
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	number, fenced := 0, false
	for scanner.Scan() {
		line := scanner.Text()
		number++
		trimmed := strings.TrimSpace(line)
		fence := strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
		if fence {
			fenced = !fenced
		}
		if fenced || fence {
			// Markers and headings in code blocks are examples, not structure.
			if current != nil && current.Title != "" {
				section, body, header = append(section, line), append(body, line), false
			}
			continue
		}
		if m := markerPattern.FindStringSubmatch(line); m != nil {
			flush()
			current, level, header = &Contract{ID: m[1], Path: path, Line: number}, 0, false
			continue
		}
		if current == nil {
			continue
		}
		heading := headingPat.FindStringSubmatch(line)
		if current.Title == "" {
			if heading != nil {
				current.Title, level, header = heading[2], len(heading[1]), true
				section = append(section, line)
			}
			continue
		}
		if anyMarker.MatchString(line) || (heading != nil && len(heading[1]) <= level) {
			flush()
			continue
		}
		section = append(section, line)
		if header {
			if m := headerPattern.FindStringSubmatch(line); m != nil {
				switch m[1] {
				case "kind":
					current.Kind = m[2]
				case "scope":
					current.Scope = m[2]
				case "expiry":
					current.Expiry = m[2]
				case "review":
					current.Review = m[2]
				}
				continue
			}
			if strings.TrimSpace(line) == "" && current.Kind == "" {
				continue
			}
			header = false
		}
		body = append(body, line)
	}
	flush()
	return result, problems
}
