# Continuity bootstrap

Project: example

## Continuity rules (built in)

- discovery: Before diagnosing or redesigning existing behavior, search Continuity history with focused terms (continuity_history_search), then expand only relevant refs. Reuse loaded evidence; no useful match means proceed to code. History is prior intent, not proof or authority: verify it against current code. Mechanical edits need no search.
- task-identity: Checkpoint under your own task_id, continuing from its current tip. To review or repair another agent's task, start a new task_id with reviews_task_id; never write to another agent's task or its actions.
- own-commits: Narrate only commits your session or task made; a commit attributed to another task belongs to its agent.
- checkout-lifecycle: For new and resumed tasks, default to the current checkout and branch for sequential single-session work. Create a task branch/worktree only for confirmed concurrent writers, explicit isolation, or a required project workflow. Open tasks, leases, branches and expiring presence alone do not prove concurrency; check continuity_presence and Git state when uncertain, and respect the user's single-session report. After integration, remove the task worktree AND local branch only if clean, with no unique commits or live writer; otherwise report why retained.

Suggested conversation title: example — Continue scaffold

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

_Additional instructions are available on demand._
