package portable

import (
	"context"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// RecoveryComparison chooses only the canonical comparison reader. The guarded
// mode requires a caller-owned transaction and a complete reference baseline
// whose validated recovery metadata remains fenced by the canonical journal.
// The consumer must verify every canonical side effect after application and
// roll back on any recovery mutation before committing the staged result.
// It never authorizes partial application of a recovery observation.
type RecoveryComparison uint8

const (
	RecoveryComparisonComplete RecoveryComparison = iota
	RecoveryComparisonGuarded
)

type recoveryComparisonSource struct{ queryer }

func recoveryComparisonReader(source queryer, mode RecoveryComparison) (queryer, error) {
	switch mode {
	case RecoveryComparisonComplete:
		return source, nil
	case RecoveryComparisonGuarded:
		return recoveryComparisonSource{source}, nil
	default:
		return nil, fmt.Errorf("unknown recovery comparison scope")
	}
}

func recoverySelectionAllowed(ctx context.Context, source queryer, project string, identities []Envelope) (bool, error) {
	if !guardedRecoveryComparison(source) {
		var found bool
		err := source.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM redaction_recoveries WHERE project_id=?)`, project).Scan(&found)
		return !found, err
	}
	seen := map[string]bool{}
	for _, e := range identities {
		key := recordKey(e)
		if seen[key] {
			continue
		}
		seen[key] = true
		var found bool
		syncwork.RecordWork(ctx, "recovery_identity_probes", 1)
		err := source.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM redaction_recoveries INDEXED BY redaction_recoveries_identity WHERE project_id=? AND record_type=? AND record_id=? AND record_revision=?)`, project, e.RecordType, e.RecordID, e.RecordRevision).Scan(&found)
		if err != nil || found {
			return false, err
		}
	}
	return true, nil
}

func guardedRecoveryComparison(source queryer) bool {
	switch value := source.(type) {
	case recoveryComparisonSource:
		return true
	case *recordSelectionSource:
		return guardedRecoveryComparison(value.queryer)
	default:
		return false
	}
}
