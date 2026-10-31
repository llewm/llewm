package portable

import (
	"context"
	"database/sql"
)

// LocalEnvelopesForImport reads the same complete operational record set used
// by import planning, including unreferenced invocations. The caller owns the
// transaction, which can protect a comparison against concurrent local writes.
func LocalEnvelopesForImport(ctx context.Context, tx *sql.Tx, projectID string) (map[string]EncodedEnvelope, error) {
	identity, err := identityFrom(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	root, err := projectRootFrom(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	var schema string
	if err := tx.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&schema); err != nil {
		return nil, err
	}
	validated := make(snapshotEncodings)
	specs, _, err := loadOperationalSpecs(ctx, tx, projectID, root, identity.LineageID, true, validated)
	if err != nil {
		return nil, err
	}
	result := make(map[string]EncodedEnvelope, len(specs))
	for _, spec := range specs {
		encoded, err := validated.encode(ctx, spec)
		if err != nil {
			return nil, err
		}
		result[recordKey(encoded.Envelope)] = encoded
	}
	return result, nil
}
