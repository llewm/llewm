package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/decision/decisionhash"
)

// migrationSteps run in Go after a migration's SQL, inside the same
// transaction, for conversions SQL cannot express (such as recomputing
// semantic hashes).
var migrationSteps = map[string]func(context.Context, *sql.Tx) error{
	"056_work_items.sql": rehashDecisions,
}

// migrationPreSteps run before a migration's SQL, inside the same
// transaction, to refuse a migration that would lose data.
var migrationPreSteps = map[string]func(context.Context, *sql.Tx) error{
	"057_cut_evidence_and_cards.sql": requireEvidenceNarrated,
}

func applyMigration(ctx context.Context, tx *sql.Tx, name string) error {
	body, err := migrations.ReadFile("migrations/" + name)
	if err != nil {
		return err
	}
	if step := migrationPreSteps[name]; step != nil {
		if err := step(ctx, tx); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if step := migrationSteps[name]; step != nil {
		if err := step(ctx, tx); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, name, nowText())
	return err
}

// rehashDecisions recomputes record_sha256 for decisions whose stored hash no
// longer matches their record and links, after link target kinds changed.
func rehashDecisions(ctx context.Context, tx *sql.Tx) error {
	type row struct {
		record decisionhash.Fields
		hash   string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,project_id,record_sha256,title,rationale,disposition,actor,actor_type,authority,review_source,COALESCE(supersedes_id,'') FROM decision_records ORDER BY project_id,id`)
	if err != nil {
		return err
	}
	var values []row
	for rows.Next() {
		var value row
		v := &value.record
		if err := rows.Scan(&v.ID, &v.ProjectID, &value.hash, &v.Title, &v.Rationale, &v.Disposition, &v.Actor, &v.ActorType, &v.Authority, &v.ReviewSource, &v.SupersedesID); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	changed := 0
	for _, value := range values {
		v := value.record
		v.Links = []decisionhash.Link{}
		links, err := tx.QueryContext(ctx, `SELECT kind,target_kind,target_id,target_revision,authority FROM decision_links WHERE project_id=? AND decision_id=? ORDER BY ordinal`, v.ProjectID, v.ID)
		if err != nil {
			return err
		}
		for links.Next() {
			var link decisionhash.Link
			if err := links.Scan(&link.Kind, &link.TargetKind, &link.TargetID, &link.TargetRevision, &link.Authority); err != nil {
				links.Close()
				return err
			}
			v.Links = append(v.Links, link)
		}
		links.Close()
		if err := links.Err(); err != nil {
			return err
		}
		hash, err := decisionhash.Hash(v)
		if err != nil {
			return err
		}
		if hash == value.hash {
			continue
		}
		if changed == 0 {
			if _, err := tx.ExecContext(ctx, `DROP TRIGGER decision_records_no_update`); err != nil {
				return err
			}
		}
		changed++
		if _, err := tx.ExecContext(ctx, `UPDATE decision_records SET record_sha256=? WHERE project_id=? AND id=?`, hash, v.ProjectID, v.ID); err != nil {
			return err
		}
	}
	if changed > 0 {
		if _, err := tx.ExecContext(ctx, `CREATE TRIGGER decision_records_no_update BEFORE UPDATE ON decision_records BEGIN SELECT RAISE(ABORT,'decision history is append-only'); END`); err != nil {
			return err
		}
	}
	return nil
}

// requireEvidenceNarrated refuses to drop evidence records whose commit has no
// work narrative, and links that name evidence records or reference cards.
func requireEvidenceNarrated(ctx context.Context, tx *sql.Tx) error {
	var id, commit string
	err := tx.QueryRowContext(ctx, `SELECT e.id,e.commit_id FROM evidence_records e WHERE NOT EXISTS(SELECT 1 FROM work_narrative_anchors a WHERE a.project_id=e.project_id AND a.anchor_kind='commit' AND a.target=e.commit_id) ORDER BY e.recorded_at,e.id LIMIT 1`).Scan(&id, &commit)
	if err == nil {
		return fmt.Errorf("evidence record %s (commit %s) has no work narrative; run llewm narrative import-git-notes or record one with the previous build first", id, commit)
	}
	if err != sql.ErrNoRows {
		return err
	}
	var links int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM decision_links WHERE target_kind='evidence')+(SELECT COUNT(*) FROM topic_links WHERE target_kind='card')`).Scan(&links); err != nil {
		return err
	}
	if links > 0 {
		return fmt.Errorf("%d decision or topic links name evidence records or reference cards; supersede them with the previous build first", links)
	}
	return nil
}
