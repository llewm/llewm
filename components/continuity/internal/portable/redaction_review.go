package portable

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RedactionReview is a host-local acceptance of one source's redaction
// projection. It holds hashes and review provenance only. It never replaces
// the source observation: import still retains the exact source envelope, so
// this host exports and replays the same bytes as every other host.
type RedactionReview struct {
	SourceSHA256    string    `json:"source_sha256"`
	ProjectedSHA256 string    `json:"projected_sha256"`
	RecordType      string    `json:"record_type"`
	RecordID        string    `json:"record_id"`
	RecordRevision  int       `json:"record_revision"`
	Reviewer        string    `json:"reviewer"`
	Reason          string    `json:"reason"`
	ReviewedAt      time.Time `json:"reviewed_at"`
}

type execQueryer interface {
	queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// ReviewRedactionProjection records the reviewer's acceptance of the projection
// of exactly source. The projection is recomputed here, so a review can only
// name the deterministic interpretation of those bytes. Repeating an identical
// review is a no-op; a review that would name a different projection refuses.
func ReviewRedactionProjection(ctx context.Context, db execQueryer, projectID string, source []byte, reviewer, reason string, at time.Time) (RedactionReview, error) {
	reviewer, reason = strings.TrimSpace(reviewer), strings.TrimSpace(reason)
	if reviewer == "" || reason == "" {
		return RedactionReview{}, fmt.Errorf("a redaction review needs a reviewer and a reason")
	}
	projection, err := PreviewRedactionProjection(source)
	if err != nil {
		return RedactionReview{}, err
	}
	e := projection.Projected.Envelope
	review := RedactionReview{SourceSHA256: projection.SourceSHA256, ProjectedSHA256: e.EnvelopeSHA256, RecordType: e.RecordType, RecordID: e.RecordID, RecordRevision: e.RecordRevision, Reviewer: reviewer, Reason: reason, ReviewedAt: at.UTC()}
	var held string
	err = db.QueryRowContext(ctx, `SELECT projected_sha256 FROM redaction_projection_reviews WHERE project_id=? AND source_sha256=?`, projectID, review.SourceSHA256).Scan(&held)
	switch {
	case err == nil && held == review.ProjectedSHA256:
		return review, nil
	case err == nil:
		return RedactionReview{}, fmt.Errorf("source %s already has a review for a different projection", review.SourceSHA256)
	case !errors.Is(err, sql.ErrNoRows):
		return RedactionReview{}, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO redaction_projection_reviews(project_id,source_sha256,projected_sha256,record_type,record_id,record_revision,reviewer,reason,reviewed_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		projectID, review.SourceSHA256, review.ProjectedSHA256, review.RecordType, review.RecordID, review.RecordRevision, review.Reviewer, review.Reason, review.ReviewedAt.Format(time.RFC3339Nano))
	if err != nil {
		return RedactionReview{}, fmt.Errorf("record redaction review: %w", err)
	}
	return review, nil
}

// reviewedProjections maps reviewed source envelope hashes to the projected
// envelope hash each review accepted.
func reviewedProjections(ctx context.Context, source queryer, projectID string) (map[string]string, error) {
	rows, err := source.QueryContext(ctx, `SELECT source_sha256,projected_sha256 FROM redaction_projection_reviews WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reviewed := map[string]string{}
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		reviewed[from] = to
	}
	return reviewed, rows.Err()
}

// admittedForm is the envelope that import decodes for record. A reviewed
// source decodes as its projection, but only when recomputing the projection
// from these exact bytes reproduces the reviewed hash. Source hash alone is
// never proof. A source that now passes admission decodes as itself.
func admittedForm(record EncodedEnvelope, reviewed map[string]string) (EncodedEnvelope, error) {
	want, ok := reviewed[record.Envelope.EnvelopeSHA256]
	if !ok {
		return record, nil
	}
	projection, err := PreviewRedactionProjection(record.Bytes)
	if errors.Is(err, ErrSourceAdmissible) {
		return record, nil
	}
	if err != nil || projection.SourceSHA256 != record.Envelope.EnvelopeSHA256 || projection.Projected.Envelope.EnvelopeSHA256 != want {
		// Name only the hash: source identity or content may hold a credential.
		return EncodedEnvelope{}, fmt.Errorf("reviewed redaction projection of source %s no longer matches its review; review it again", record.Envelope.EnvelopeSHA256)
	}
	return projection.Projected, nil
}
