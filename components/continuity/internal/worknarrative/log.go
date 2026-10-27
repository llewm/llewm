package worknarrative

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// LogOptions controls the Git selection and narrative presentation.
type LogOptions struct {
	Member, Revision, Author, Since, Until string
	Paths                                  []string
	Limit                                  int
	Oneline, Expand, Missing               bool
}

// Log renders Git commit metadata and current commit-linked narratives. Git notes
// are deliberately excluded: narratives are the canonical source of sections.
func (s Service) Log(ctx context.Context, projectID string, options LogOptions) (string, error) {
	if options.Limit < 1 || options.Limit > 1000 {
		return "", fmt.Errorf("limit must be between 1 and 1000")
	}
	repository, err := s.repository(options.Member)
	if err != nil {
		return "", err
	}
	ref := options.Revision
	if ref == "" {
		ref = "HEAD"
	}
	// Resolve each endpoint to an exact commit, including both supported ranges.
	separator := ""
	if strings.Contains(ref, "...") {
		separator = "..."
	} else if strings.Contains(ref, "..") {
		separator = ".."
	}
	parts := []string{ref}
	if separator != "" {
		parts = strings.Split(ref, separator)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid revision range")
		}
	}
	for i, part := range parts {
		if part == "" {
			part = "HEAD"
		}
		if strings.HasPrefix(part, "-") {
			return "", fmt.Errorf("revision cannot start with -")
		}
		resolved, err := s.git(ctx, repository.Root, "rev-parse", "--verify", "--end-of-options", part+"^{commit}")
		if err != nil {
			return "", err
		}
		parts[i] = strings.TrimSpace(string(resolved))
	}
	args := []string{"-c", "color.ui=false", "log", "--no-notes", "--format=%H%x00%an <%ae>%x00%ad%x00%B%x00", "--date=default"}
	if !options.Missing {
		args = append(args, fmt.Sprintf("-n%d", options.Limit))
	}
	if options.Author != "" {
		args = append(args, "--author="+options.Author)
	}
	if options.Since != "" {
		args = append(args, "--since="+options.Since)
	}
	if options.Until != "" {
		args = append(args, "--until="+options.Until)
	}
	args = append(args, strings.Join(parts, separator), "--")
	args = append(args, options.Paths...)
	output, err := s.git(ctx, repository.Root, args...)
	if err != nil {
		return "", err
	}
	var result strings.Builder
	shown := 0
	for _, entry := range strings.Split(string(output), "\x00\n") {
		entry = strings.TrimPrefix(entry, "\n")
		if strings.TrimSpace(entry) == "" {
			continue
		}
		fields := strings.SplitN(strings.TrimSuffix(entry, "\x00"), "\x00", 4)
		if len(fields) != 4 {
			return "", fmt.Errorf("unexpected git log output")
		}
		hash := fields[0]

		rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT w.payload_json FROM work_narratives w JOIN work_narrative_anchors a ON a.project_id=w.project_id AND a.narrative_id=w.id WHERE w.project_id=? AND a.anchor_kind='commit' AND a.target=? AND COALESCE(a.region_locator,'')=? AND NOT EXISTS (SELECT 1 FROM superseded_records x WHERE x.project_id=w.project_id AND x.family='narrative' AND x.id=w.id) ORDER BY w.recorded_at, w.id`, projectID, hash, MemberLocator(repository.Member))
		if err != nil {
			return "", err
		}
		var records []Record
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return "", err
			}
			var record Record
			if err = json.Unmarshal([]byte(raw), &record); err != nil {
				rows.Close()
				return "", err
			}
			records = append(records, record)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
		if options.Missing && len(records) != 0 {
			continue
		}
		if shown >= options.Limit {
			break
		}
		shown++
		if options.Oneline {
			subject, _, _ := strings.Cut(fields[3], "\n")
			fmt.Fprintf(&result, "%s %s", hash[:min(7, len(hash))], subject)
			if options.Missing {
				result.WriteString(" [no narrative]")
			}
			result.WriteString("\n")
		} else {
			fmt.Fprintf(&result, "commit %s\nAuthor: %s\nDate:   %s\n\n", hash, fields[1], fields[2])
			for _, line := range strings.Split(strings.TrimRight(fields[3], "\n"), "\n") {
				fmt.Fprintf(&result, "    %s\n", line)
			}
			if options.Missing {
				result.WriteString("\n    [no current narrative]\n")
			}
		}
		if len(records) > 0 && (!options.Oneline || options.Expand) {
			result.WriteString("\nNarratives:\n")
			for _, record := range records {
				for _, line := range strings.Split(strings.TrimRight(RenderNote(hash, record), "\n"), "\n") {
					fmt.Fprintf(&result, "    %s\n", line)
				}
				result.WriteString("\n")
			}
		} else if !options.Oneline {
			result.WriteString("\n")
		}
	}
	return result.String(), nil
}
