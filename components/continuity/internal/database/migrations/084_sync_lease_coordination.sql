-- Coordination leases travel through their own Git refs. Their grants,
-- renewals and remote mirror receipts are not curated history inputs and must
-- not invalidate a completed history cycle (including that cycle's own lease
-- reconciliation). Exported lease stamps and stale-write resolutions remain
-- tracked: those are canonical history observations.
DROP TRIGGER history_sync_lease_grants_insert;
DROP TRIGGER history_sync_lease_grants_update;
DROP TRIGGER history_sync_lease_grants_delete;
DROP TRIGGER history_sync_lease_events_insert;
DROP TRIGGER history_sync_lease_events_update;
DROP TRIGGER history_sync_lease_events_delete;
DROP TRIGGER history_sync_lease_remote_events_insert;
DROP TRIGGER history_sync_lease_remote_events_update;
DROP TRIGGER history_sync_lease_remote_events_delete;
