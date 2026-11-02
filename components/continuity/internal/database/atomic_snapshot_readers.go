package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
)

// These two deterministic read views were last replaced by migration 074.
// Later view changes deliberately disable snapshot reuse until reviewed. Keep
// exact SQL, including string literals; whitespace inside SQL literals matters.
var atomicSnapshotViews = sync.OnceValues(func() (map[string]string, error) {
	body, err := migrations.ReadFile("migrations/074_stable_revision_fork_heads.sql")
	if err != nil {
		return nil, err
	}
	definitions := map[string]string{}
	for _, name := range []string{"fork_rejected_records", "superseded_records"} {
		marker := "CREATE VIEW " + name + " AS\n"
		start := strings.Index(string(body), marker)
		if start < 0 {
			return nil, fmt.Errorf("missing reviewed snapshot view %s", name)
		}
		tail := string(body[start:])
		end := strings.IndexByte(tail, ';')
		if end < 0 {
			return nil, fmt.Errorf("unterminated reviewed snapshot view %s", name)
		}
		definitions[name] = strings.TrimSpace(tail[:end])
	}
	return definitions, nil
})

// HistoryAtomicSnapshotReaders rejects unknown/damaged read views and TEMP
// shadows before pure current-state snapshot reuse. It grants no semantic or
// journal receipt and never repairs a schema. Mutation tracking is checked
// independently by the caller.
func HistoryAtomicSnapshotReaders(ctx context.Context, source interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (bool, error) {
	expected, err := atomicSnapshotViews()
	if err != nil {
		return false, err
	}
	rows, err := source.QueryContext(ctx, `SELECT 'main',name,COALESCE(sql,'') FROM main.sqlite_schema WHERE type='view' UNION ALL SELECT 'temp',name,COALESCE(sql,'') FROM temp.sqlite_schema WHERE type IN ('table','view')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		var schema, name, body string
		if err := rows.Scan(&schema, &name, &body); err != nil {
			return false, err
		}
		want, ok := expected[name]
		if schema != "main" || !ok || strings.TrimSuffix(strings.TrimSpace(body), ";") != want {
			return false, nil
		}
		found++
	}
	return found == len(expected), rows.Err()
}
