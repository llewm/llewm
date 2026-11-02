package database

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// Pin the reviewed INSERT effect graph: membership -> canonical journal /
// inventory token -> canonical generation. Future trigger revisions must fall
// back until this batching contract is reviewed; do not use latest definitions.
var membershipBatchDefinitions = sync.OnceValues(func() (map[string]string, error) {
	wanted := map[string]bool{
		"history_index_collect_envelope":                  true,
		"history_sync_history_index_block_members_insert": true, "history_sync_history_index_block_members_update": true, "history_sync_history_index_block_members_delete": true,
		"history_inventory_history_index_block_members_insert": true, "history_inventory_history_index_block_members_update": true, "history_inventory_history_index_block_members_delete": true,
		"history_journal_insert": true, "history_journal_update": true, "history_journal_delete": true,
	}
	result := make(map[string]string, len(wanted))
	for _, name := range []string{"086_history_change_journal.sql", "089_normalized_history_index.sql", "092_history_inventory_generation.sql"} {
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		for _, match := range storageTriggerPattern.FindAllStringSubmatch(string(body), -1) {
			if wanted[match[1]] {
				result[match[1]] = compactTriggerSQL(match[0])
			}
		}
	}
	if len(result) != len(wanted) {
		return nil, fmt.Errorf("membership batching has incomplete compiled effects")
	}
	return result, nil
})

// HistoryIndexMembershipBatching allows batching only when membership INSERTs
// cannot change indexed bodies/references or invoke application-defined effects.
// Check the complete trigger closure in the caller's writer transaction. Unknown
// main/TEMP triggers, missing guards or changed definitions forbid batching;
// this check never repairs schema or grants a reusable receipt.
func HistoryIndexMembershipBatching(ctx context.Context, tx *sql.Tx) (bool, error) {
	expected, err := membershipBatchDefinitions()
	if err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,sql,0 FROM sqlite_schema WHERE type='trigger' AND lower(tbl_name) IN ('history_index_block_members','history_sync_changes','history_sync_generation','history_index_inventory_generation') UNION ALL SELECT name,sql,1 FROM sqlite_temp_schema WHERE type='trigger' AND lower(tbl_name) IN ('history_index_block_members','history_sync_changes','history_sync_generation','history_index_inventory_generation')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name, body string
		var temporary bool
		if err := rows.Scan(&name, &body, &temporary); err != nil {
			return false, err
		}
		if temporary || expected[name] == "" || expected[name] != compactTriggerSQL(body) {
			return false, nil
		}
		count++
	}
	return count == len(expected), rows.Err()
}
