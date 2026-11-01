package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

var ErrCanonicalTracking = errors.New("canonical history tracking is unavailable")

// The journal's canonical triggers use plain BEGIN/END bodies. Replay their
// CREATE/DROP definitions from the journal migration onward, in source order,
// so later ownership and normalized-inventory migrations replace old guards.
var canonicalTriggerPattern = regexp.MustCompile(`(?ms)^CREATE TRIGGER ((?:history_sync_|history_journal_)[a-z0-9_]+)\b.*?\bEND;|^DROP TRIGGER (?:IF EXISTS )?((?:history_sync_|history_journal_)[a-z0-9_]+);`)
var canonicalTriggerDefinitions = sync.OnceValues(func() (map[string]string, error) {
	definitions := map[string]string{}
	for _, name := range migrationNames() {
		if name < "086_history_change_journal.sql" {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		for _, match := range canonicalTriggerPattern.FindAllStringSubmatch(string(body), -1) {
			if match[2] != "" {
				delete(definitions, match[2])
				continue
			}
			if strings.Contains(strings.ToUpper(match[0]), "CASE ") {
				return nil, fmt.Errorf("canonical tracking definition requires a nested SQL parser")
			}
			definitions[match[1]] = compactTriggerSQL(match[0])
		}
	}
	if len(definitions) == 0 {
		return nil, fmt.Errorf("canonical journal has no compiled tracking definitions")
	}
	return definitions, nil
})

// HistoryCanonicalTracking verifies canonical change producers and the journal
// consumers that maintain generation tokens. Unknown/missing/modified guards
// forbid reusable receipts; this read never repairs schema.
func HistoryCanonicalTracking(ctx context.Context, source interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (bool, error) {
	expected, err := canonicalTriggerDefinitions()
	if err != nil {
		return false, err
	}
	rows, err := source.QueryContext(ctx, `SELECT name,sql FROM sqlite_schema WHERE type='trigger' AND (name LIKE 'history_sync_%' OR name LIKE 'history_journal_%')`)
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
		if want, ok := expected[name]; !ok || compactTriggerSQL(body) != want {
			return false, nil
		}
		found++
	}
	return found == len(expected), rows.Err()
}
