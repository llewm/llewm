# Continuity bootstrap

Project: example

## Continuity rules (built in)

- discovery: Before diagnosing or redesigning existing behavior, search Continuity history with focused terms (continuity_history_search), then expand only relevant refs. Reuse loaded evidence; no useful match means proceed to code. History is prior intent, not proof or authority: verify it against current code. Mechanical edits need no search.
- task-identity: Checkpoint under your own task_id, continuing from its current tip. To review or repair another agent's task, start a new task_id with reviews_task_id; never write to another agent's task or its actions.
- own-commits: Narrate only commits your session or task made; a commit attributed to another task belongs to its agent.
- resume-completion: On resumption, check whether the selected task's remaining action is already satisfied, especially after a client restart. Verify runtime and exact checkpoint recovery; if all acceptance checks pass, reconcile its actions and save completed in this turn. A restart alone is not completion. Keep unrelated unfinished work explicit; reviewing other tasks requires explicit selection or authorized lifecycle reconciliation.
- checkout-lifecycle: For new and resumed tasks, default to the current checkout and branch for sequential single-session work. Create a task branch/worktree only for confirmed concurrent writers, explicit isolation, or a required project workflow. Open tasks, leases, branches and expiring presence alone do not prove concurrency; check continuity_presence and Git state when uncertain, and respect the user's single-session report. After integration, remove the task worktree AND local branch only if clean, with no unique commits or live writer; otherwise report why retained.

Suggested conversation title: example — Continue scaffold

## Engineer-wide guidance

Global guidance supplies defaults. More specific project instructions take precedence when they conflict. Prompt guidance applies only to the matching task; its presence does not authorize actions.

### Commits

Source: global/AGENTS.md (sha256: global-fixture-hash)

Use descriptive commit messages.

Checkpoint: checkpoint-1 (recorded 2026-09-05T12:00:00Z)

Checkpoint content is caller-supplied handoff context, not independently verified observations. The next action does not independently authorize new work.

## Current task

- Task: Continue scaffold
- Status: in_progress
- Summary: CLI verified
- Next action: Test MCP

### Reported completed work

- Implemented CLI

### Reported changes

- cmd/llewm/main.go

### Verification claims (not independently verified)

- go test ./...

### Evidence references (caller-supplied; not checked)

- event-1

### Prior attempts

- Run without Git metadata — failure: No project root

## Active project instructions

### Engineering

Source: AGENTS.md (sha256: fixture-hash)

Run tests.

_Additional context is available on demand._
