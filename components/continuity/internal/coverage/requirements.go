package coverage

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	markerPattern = regexp.MustCompile(`^<!--\s*continuity:requirement\s+([A-Za-z0-9][A-Za-z0-9._-]*)\s*-->\s*$`)
	titlePattern  = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	scopePattern  = regexp.MustCompile(`^scope:\s*(.+)$`)
)

// ParseRequirements reads marked sections. A section starts at
// `<!-- continuity:requirement ID -->`, takes the next heading as its title,
// an optional `scope: a, b` line right after the title, and the following text
// up to the next marker as its body. Text before the first marker is ignored.
// Problems describe duplicate IDs and sections without a title; the sections
// that parse are still returned.
func ParseRequirements(text string) ([]Requirement, []string) {
	var (
		reqs     []Requirement
		problems []string
		current  *Requirement
		body     []string
		seen     = map[string]bool{}
		afterH   bool
	)
	flush := func() {
		if current == nil {
			return
		}
		current.Body = strings.TrimSpace(strings.Join(body, "\n"))
		switch {
		case current.Title == "":
			problems = append(problems, fmt.Sprintf("requirement %q has no heading", current.ID))
		case seen[current.ID]:
			problems = append(problems, fmt.Sprintf("duplicate requirement %q", current.ID))
		default:
			seen[current.ID] = true
			reqs = append(reqs, *current)
		}
		current, body = nil, nil
	}
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if m := markerPattern.FindStringSubmatch(line); m != nil {
			flush()
			current, afterH = &Requirement{ID: m[1]}, false
			continue
		}
		if current == nil {
			continue
		}
		if current.Title == "" {
			if m := titlePattern.FindStringSubmatch(line); m != nil {
				current.Title, afterH = m[1], true
			}
			continue
		}
		if afterH {
			afterH = false
			if m := scopePattern.FindStringSubmatch(line); m != nil {
				for _, part := range strings.Split(m[1], ",") {
					if part = strings.TrimSpace(part); part != "" {
						current.Scope = append(current.Scope, part)
					}
				}
				continue
			}
		}
		body = append(body, line)
	}
	flush()
	return reqs, problems
}

// LoadRequirements reads and parses the requirements file under root. A
// missing file is a problem, not an error, so a report still lists orphans.
func LoadRequirements(root, path string) ([]Requirement, []string) {
	if path == "" {
		path = DefaultRequirementsPath
	}
	data, err := os.ReadFile(resolve(root, path))
	if err != nil {
		return nil, []string{fmt.Sprintf("requirements file %s is unreadable: %v", path, err)}
	}
	return ParseRequirements(string(data))
}
