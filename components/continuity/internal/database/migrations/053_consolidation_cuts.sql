-- Consolidation, slice 6e1 of continuity-multi-repo-sync: drop the storage of
-- the local-only families removed from the code (docs/consolidation-review.md).
-- None of these tables is portable, and no kept table, view, or trigger
-- references them. Only autonext held rows (runs that ended 2026-09-25); the
-- pre-migration backup keeps them.

-- Autonext continuation control, replaced by standing grants.
DROP TABLE autonext_codex_prompt_receipts;
DROP TABLE autonext_codex_claims;
DROP TABLE autonext_codex_bindings;
DROP TABLE autonext_budget_observations;
DROP TABLE autonext_step_transitions;
DROP TABLE autonext_run_events;
DROP TABLE autonext_runs;
DROP TABLE autonext_grant_events;
DROP TABLE autonext_grants;

-- Completion profiles and receipts; a completed checkpoint is the terminal step.
DROP TABLE completion_stage_receipts;
DROP TABLE completion_profile_procedure_bindings;
DROP TABLE completion_profiles;

-- Policy references, incidents, restrictions, and action policies; never
-- populated. rule_sections, environment_facts, and procedure_candidates were
-- created early and never written by any code.
DROP TABLE restriction_events;
DROP TABLE action_restrictions;
DROP TABLE incident_events;
DROP TABLE instruction_incidents;
DROP TABLE action_policy_documents;
DROP TABLE policy_references;
DROP TABLE rule_sections;
DROP TABLE environment_facts;
DROP TABLE procedure_candidates;

-- Roadmap authority migrations; never activated.
DROP TABLE roadmap_authority_destinations;
DROP TABLE roadmap_authority_sources;
DROP TABLE roadmap_authority_migrations;
