INSERT INTO checkpoint_action_transitions(id, action_id, status, reason, created_at)
SELECT 'action-backfill-superseded-' || action.id,
       action.id,
       'superseded',
       'Historical checkpoint predates explicit action lifecycle; only the latest pre-lifecycle action remains unresolved.',
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM checkpoint_actions action
JOIN checkpoints source ON source.id = action.source_checkpoint_id
WHERE action.id = 'action-' || action.source_checkpoint_id
  AND EXISTS (
      SELECT 1 FROM checkpoints newer
      WHERE newer.project_id = source.project_id
        AND (newer.created_at > source.created_at OR (newer.created_at = source.created_at AND newer.id > source.id))
  );
