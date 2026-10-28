-- Semantic replay proofs are disposable, rechecked against canonical records
-- before use, and do not change the inputs of a completed transport cycle.
-- Refreshing them during import must not invalidate that cycle's own proof.
-- Keep block/record/ref inventory and endpoint publication membership tracked:
-- damage or pending delivery there still requires a complete transport cycle.
DROP TRIGGER history_sync_history_index_valid_sets_insert;
DROP TRIGGER history_sync_history_index_valid_sets_update;
DROP TRIGGER history_sync_history_index_valid_sets_delete;
DROP TRIGGER history_sync_history_index_narrative_tip_insert;
DROP TRIGGER history_sync_history_index_narrative_tip_update;
DROP TRIGGER history_sync_history_index_narrative_tip_delete;
DROP TRIGGER history_sync_history_index_narrative_members_insert;
DROP TRIGGER history_sync_history_index_narrative_members_update;
DROP TRIGGER history_sync_history_index_narrative_members_delete;
DROP TRIGGER history_sync_history_index_mixed_tip_insert;
DROP TRIGGER history_sync_history_index_mixed_tip_update;
DROP TRIGGER history_sync_history_index_mixed_tip_delete;
DROP TRIGGER history_sync_history_index_mixed_members_insert;
DROP TRIGGER history_sync_history_index_mixed_members_update;
DROP TRIGGER history_sync_history_index_mixed_members_delete;
