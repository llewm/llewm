# Continuity bootstrap

Project: example

## Continuity rules (built in)

- discovery: Before diagnosing or redesigning existing behavior, search history with focused terms (continuity_history_search) and expand only relevant refs. Reuse loaded evidence; no useful match means go to code. History is prior intent, not proof or authority; verify it against current code. Skip for mechanical edits.
- task-identity: Checkpoint under your own task_id from its current tip. To review or repair another agent's task, use a new task_id with reviews_task_id; never write to another agent's task or actions.
- own-commits: Narrate only commits your session or task made; another task's commits belong to its agent.
- resume-completion: On resumption, especially after a client restart, check whether the selected task's remaining action is already done: verify runtime and exact checkpoint recovery. If all acceptance checks pass, reconcile its actions and save completed this turn. A restart alone is not completion. Keep unrelated unfinished work explicit; review other tasks only when explicitly selected or authorized for lifecycle reconciliation.
- checkout-lifecycle: Default new and resumed sequential single-session tasks to the current checkout and branch. Create a task branch/worktree only for confirmed concurrent writers, explicit isolation, or a required project workflow; open tasks, leases, branches and expiring presence alone do not prove concurrency. If unsure, check continuity_presence and Git state; respect the user's single-session report. After integration, remove the task worktree AND local branch only if clean, with no unique commits or live writer; otherwise report why kept.

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
