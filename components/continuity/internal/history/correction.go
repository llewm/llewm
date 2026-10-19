package history

import (
	"context"
	"strings"
)

// A history is append-only, but current retrieval must not silently promote a
// corrected or rejected claim. Versioned operational records keep their own
// lifecycle semantics; this predicate covers narrative/decision supersession.
const supersededChunkSQL = `EXISTS (SELECT 1 FROM superseded_records s
 WHERE s.project_id=history_chunks.project_id AND (
 (history_chunks.family='narrative_claim' AND s.family='narrative' AND s.id=substr(history_chunks.ref,11,instr(history_chunks.ref,'#')-11)) OR
 (history_chunks.family='decision' AND s.family='decision' AND s.id=substr(history_chunks.ref,10))))`

func correction(ctx context.Context, db queryer, projectID, ref string) (string, []string, bool, error) {
	family, id, _ := strings.Cut(ref, ":")
	if family != "narrative" && family != "decision" {
		return "current", nil, false, nil
	}
	if family == "narrative" {
		id, _, _ = strings.Cut(id, "#")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM superseded_records WHERE project_id=? AND family=? AND id=?`, projectID, family, id).Scan(&count); err != nil {
		return "", nil, false, err
	}
	if count == 0 {
		return "current", nil, false, nil
	}
	table := "work_narratives"
	if family == "decision" {
		table = "decision_records"
	}
	rows, err := db.QueryContext(ctx, `SELECT id FROM `+table+` WHERE project_id=? AND supersedes_id=? ORDER BY recorded_at,id LIMIT 65`, projectID, id)
	if err != nil {
		return "", nil, false, err
	}
	defer rows.Close()
	var refs []string
	truncated := false
	for rows.Next() {
		var next string
		if err := rows.Scan(&next); err != nil {
			return "", nil, false, err
		}
		if len(refs) == 64 {
			truncated = true
			break
		}
		refs = append(refs, family+":"+next)
	}
	return "superseded", refs, truncated, rows.Err()
}
