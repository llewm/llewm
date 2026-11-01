package database

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// These tracking migrations use plain BEGIN/END trigger bodies. Comparing the
// installed definitions with their embedded SQL
// avoids trusting a missing or modified tracking trigger after recovery. Tests
// compare every extracted definition to a freshly initialized database.
var storageTriggerPattern = regexp.MustCompile(`(?ms)^CREATE TRIGGER ([a-z0-9_]+)\b.*?\bEND;`)
var storageTriggerDefinitions = sync.OnceValues(func() (map[string]string, error) {
	definitions := map[string]string{}
	for _, name := range []string{"091_durable_history_archive.sql", "092_history_inventory_generation.sql", "093_history_dependency_generation.sql"} {
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		matches := storageTriggerPattern.FindAllStringSubmatch(string(body), -1)
		if len(matches) == 0 {
			return nil, fmt.Errorf("storage migration has no tracking definitions")
		}
		for _, match := range matches {
			if _, exists := definitions[match[1]]; exists {
				return nil, fmt.Errorf("duplicate storage tracking definition")
			}
			definitions[match[1]] = compactTriggerSQL(match[0])
		}
	}
	return definitions, nil
})

func compactTriggerSQL(body string) string {
	return strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(body), ";")), " ")
}

// HistoryStorageTracking checks installed archive, inventory and dependency guards against
// the embedded definitions, inside the caller's transaction. A false result
// forbids delta receipts, but does not itself authorize schema repair.
func HistoryStorageTracking(ctx context.Context, tx *sql.Tx) (bool, error) {
	expected, err := storageTriggerDefinitions()
	if err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_schema WHERE type='trigger' AND (name LIKE 'history_archive_%' OR name LIKE 'history_inventory_%' OR name LIKE 'history_dependency_%' OR tbl_name IN ('history_index_dependency_records','history_index_dependencies','history_index_dependency_generation'))`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		var name, body string
		if err := rows.Scan(&name, &body); err != nil {
			return false, err
		}
		want, ok := expected[name]
		if !ok || compactTriggerSQL(body) != want {
			return false, nil
		}
		found++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return found == len(expected), nil
}
