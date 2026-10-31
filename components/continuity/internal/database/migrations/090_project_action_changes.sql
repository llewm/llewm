-- The portable transition reader scopes by its owning action. Use that same
-- ownership for the journal; a checkpoint arrival must not invalidate every
-- project's capture cursor. Missing parents still force global validation.
DROP TRIGGER history_sync_checkpoint_action_transitions_insert;
DROP TRIGGER history_sync_checkpoint_action_transitions_update;
DROP TRIGGER history_sync_checkpoint_action_transitions_delete;

CREATE TRIGGER history_sync_checkpoint_action_transitions_insert AFTER INSERT ON "checkpoint_action_transitions" BEGIN
    INSERT INTO history_sync_changes(project_id,table_name,row_key,operation)
    VALUES(COALESCE((SELECT project_id FROM checkpoint_actions WHERE id=new.action_id),''),'checkpoint_action_transitions',json_array(new."id"),'insert');
END;
CREATE TRIGGER history_sync_checkpoint_action_transitions_delete AFTER DELETE ON "checkpoint_action_transitions" BEGIN
    INSERT INTO history_sync_changes(project_id,table_name,row_key,operation)
    VALUES(COALESCE((SELECT project_id FROM checkpoint_actions WHERE id=old.action_id),''),'checkpoint_action_transitions',json_array(old."id"),'delete');
END;
CREATE TRIGGER history_sync_checkpoint_action_transitions_update AFTER UPDATE ON "checkpoint_action_transitions"
WHEN old."id" IS NOT new."id" OR old."action_id" IS NOT new."action_id" OR old."checkpoint_id" IS NOT new."checkpoint_id" OR old."status" IS NOT new."status" OR old."reason" IS NOT new."reason" OR old."created_at" IS NOT new."created_at" BEGIN
    INSERT INTO history_sync_changes(project_id,table_name,row_key,operation)
    VALUES(COALESCE((SELECT project_id FROM checkpoint_actions WHERE id=old.action_id),''),'checkpoint_action_transitions',json_array(old."id"),'update_old');
    INSERT INTO history_sync_changes(project_id,table_name,row_key,operation)
    VALUES(COALESCE((SELECT project_id FROM checkpoint_actions WHERE id=new.action_id),''),'checkpoint_action_transitions',json_array(new."id"),'update_new');
END;
