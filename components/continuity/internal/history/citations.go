package history

import (
	"context"
	"fmt"
	"strings"
)

// Citation edges are loaded without source bodies. Exact source refs provide the
// next bounded hop, rather than silently loading the whole cited bibliography.
func citationText(ctx context.Context, db queryer, projectID, narrativeID string, ordinal int) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT source_id,relation,locator FROM work_narrative_citations WHERE project_id=? AND narrative_id=? AND claim_ordinal=? ORDER BY ordinal`, projectID, narrativeID, ordinal)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var id, relation, locator string
		if err := rows.Scan(&id, &relation, &locator); err != nil {
			return "", err
		}
		state, successors, truncated, err := correction(ctx, db, projectID, "narrative:"+id)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "\nCitation: %s narrative:%s — %s (source state: %s)", relation, id, locator, state)
		if len(successors) > 0 {
			fmt.Fprintf(&out, "; successors: %s", strings.Join(successors, ", "))
		}
		if truncated {
			out.WriteString(" [successors truncated]")
		}
	}
	return out.String(), rows.Err()
}
