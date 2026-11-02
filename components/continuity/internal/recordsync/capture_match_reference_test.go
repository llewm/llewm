package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// Sequential capture before unchanged-prefix batching.
func (a Archive) referenceCaptureLocalWithin(ctx context.Context, tx *sql.Tx, objects []portable.EncodedEnvelope) error {
	if _, err := a.store().stampWithin(ctx, tx, "ready"); err != nil {
		return err
	}
	for _, object := range objects {
		decoded, err := portable.DecodeTransportEnvelope(object.Bytes)
		if err != nil || decoded.Envelope.LineageID != a.Lineage || !bytes.Equal(decoded.Bytes, object.Bytes) {
			return fmt.Errorf("invalid local archive observation")
		}
		// Any retained origin prevents a later operational export from silently
		// reclassifying a foreign observation as locally originated.
		var origin string
		err = tx.QueryRowContext(ctx, `SELECT origin_hash FROM history_archive_origins WHERE project_id=? AND envelope_hash=? ORDER BY origin_hash LIMIT 1`, a.ProjectID, decoded.Envelope.EnvelopeSHA256).Scan(&origin)
		if errors.Is(err, sql.ErrNoRows) {
			origin = Hash("local")
		} else if err != nil {
			return err
		}
		if _, err := a.store().putWithin(ctx, tx, "ready", origin, []portable.EncodedEnvelope{decoded}); err != nil {
			return err
		}
	}
	return nil
}
