package workitem

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

type answerReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ValidateAnswerTargets keeps a research answer navigable inside its project.
// External documents are cataloged as source-observation narratives first.
func ValidateAnswerTargets(ctx context.Context, db answerReader, p string, refs []string) error {
	if len(refs) == 0 {
		return fmt.Errorf("answered requires findings")
	}
	for _, ref := range refs {
		kind, id, _ := strings.Cut(ref, ":")
		var count int
		var err error
		switch kind {
		case "narrative":
			if parent, ord, ok := strings.Cut(id, "#"); ok {
				n, e := strconv.Atoi(ord)
				if e != nil || n < 0 {
					return fmt.Errorf("invalid answer claim %s", ref)
				}
				err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_narrative_claims WHERE project_id=? AND narrative_id=? AND ordinal=?`, p, parent, n).Scan(&count)
			} else {
				err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_narratives WHERE project_id=? AND id=?`, p, id).Scan(&count)
			}
		case "decision":
			err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_records WHERE project_id=? AND id=?`, p, id).Scan(&count)
		default:
			return fmt.Errorf("answer target %s must be a narrative, claim, or decision reference; catalog external documents as source observations", ref)
		}
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("answer target %s does not resolve in this project", ref)
		}
	}
	return nil
}
