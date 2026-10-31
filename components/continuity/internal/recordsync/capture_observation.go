package recordsync

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// An observation may add destination provenance to already captured local
// records. It can extend that exact capture receipt, but cannot acknowledge
// foreign arrivals, canonical changes, or missing/damaged tracking evidence.
type captureObservation struct {
	publication *capturePublication
	consumer    string
	state       string
}

// Called only immediately before an actual filesystem creation, under the
// observation's writer lock. Existing endpoint markers need no metadata walks.
func (a Archive) prepareCaptureObservation(ctx context.Context, tx *sql.Tx, project, endpoint string, objects []portable.EncodedEnvelope) *captureObservation {
	consumer, err := a.captureRoute()
	if err != nil {
		return nil
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM history_sync_cursors WHERE project_id=? AND consumer=?`, project, consumer).Scan(&state); err != nil {
		return nil
	}
	_, changes, reason, err := database.ReadSyncChangesWithin(ctx, tx, project, consumer, state, 4096)
	if err != nil || reason != "" || !captureMetadataOnly(changes) {
		return nil
	}
	stamp, before, err := directoryChangeSnapshot(ctx, a.Directory, true)
	if err != nil || stamp != state {
		return nil
	}
	p := &capturePublication{before: before, additions: map[string][]byte{}, directories: map[string]bool{}}
	for _, object := range objects {
		// This receipt accounts for new endpoint markers only. Every record
		// and its local provenance must predate the captured archive stamp.
		for _, path := range []string{portable.MemoryRecordPath(object.Envelope), filepath.Join("origins", object.Envelope.EnvelopeSHA256, Hash("local"))} {
			metadata, exists := before[path]
			if !exists || metadata.directory {
				return nil
			}
		}
		p.add(filepath.Join("origins", object.Envelope.EnvelopeSHA256, Hash(endpoint)), []byte("observed\n"))
	}
	return &captureObservation{publication: p, consumer: consumer, state: state}
}

func (o *captureObservation) complete(ctx context.Context, tx *sql.Tx, a Archive, project string) bool {
	if o == nil {
		return false
	}
	published := o.publication.completedStamp(ctx, a)
	if published == "" {
		return false
	}
	// Catch changes during the receipt's new-file byte checks as well. The
	// next capture still compares the complete metadata stamp before reuse.
	stamp, err := a.ChangeStamp(ctx)
	if err != nil || stamp != published {
		return false
	}
	// Recheck the entire bounded batch, including writes performed by SQL
	// triggers during this observation. A receipt cannot bless canonical work.
	cursor, changes, reason, err := database.ReadSyncChangesWithin(ctx, tx, project, o.consumer, o.state, 4096)
	if err != nil || reason != "" || !captureMetadataOnly(changes) {
		return false
	}
	ok, err := database.RememberSyncCursorWithin(ctx, tx, cursor, stamp)
	return err == nil && ok
}
