package worknarrative

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Log renders Git commit metadata and current commit-linked narratives. Git notes
// are deliberately excluded: narratives are the canonical source of sections.
func (s Service) Log(ctx context.Context, projectID, member, ref string, limit int) (string, error) {
	if limit < 1 || limit > 1000 {
		return "", fmt.Errorf("limit must be between 1 and 1000")
	}
	repository, err := s.repository(member)
	if err != nil {
		return "", err
	}
	if ref == "" {
		ref = "HEAD"
	}
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("revision cannot start with -")
	}
	// Resolve the revision first so it cannot be interpreted as a Git option.
	resolved, err := s.git(ctx, repository.Root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	output, err := s.git(ctx, repository.Root, "-c", "color.ui=false", "log", "--no-notes", fmt.Sprintf("-n%d", limit), "--format=%H%x00%an <%ae>%x00%ad%x00%B%x00", "--date=default", strings.TrimSpace(string(resolved)), "--")
	if err != nil {
		return "", err
	}
	var result strings.Builder
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
		fmt.Fprintf(&result, "commit %s\nAuthor: %s\nDate:   %s\n\n", hash, fields[1], fields[2])
		for _, line := range strings.Split(strings.TrimRight(fields[3], "\n"), "\n") {
			fmt.Fprintf(&result, "    %s\n", line)
		}
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
		if len(records) > 0 {
			result.WriteString("\nNarratives:\n")
			for _, record := range records {
				for _, line := range strings.Split(strings.TrimRight(RenderNote(hash, record), "\n"), "\n") {
					fmt.Fprintf(&result, "    %s\n", line)
				}
				result.WriteString("\n")
			}
		} else {
			result.WriteString("\n")
		}
	}
	return result.String(), nil
}
