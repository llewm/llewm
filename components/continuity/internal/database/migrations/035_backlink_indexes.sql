-- Exact reverse reads; no existing record or lifecycle state is changed.
CREATE INDEX reference_card_links_target
    ON reference_card_links(project_id, target_kind, target_id, target_revision, recorded_at, id);
CREATE INDEX agent_suggestions_task_origin
    ON agent_suggestions(project_id, task_id, recorded_at, id)
    WHERE task_id IS NOT NULL;
CREATE INDEX agent_suggestions_checkpoint_origin
    ON agent_suggestions(project_id, checkpoint_id, recorded_at, id)
    WHERE checkpoint_id IS NOT NULL;
CREATE INDEX agent_suggestions_idea_origin
    ON agent_suggestions(project_id, idea_id, recorded_at, id)
    WHERE idea_id IS NOT NULL;
CREATE INDEX track_plan_revisions_task_origin
    ON track_plan_revisions(project_id, task_id, recorded_at, id, revision)
    WHERE task_id IS NOT NULL;
CREATE INDEX track_plan_suggestions_target
    ON track_plan_suggestions(project_id, suggestion_id, plan_id, plan_revision);
