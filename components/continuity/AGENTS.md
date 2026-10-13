# Continuity component

These instructions supplement the root `AGENTS.md`. Paths below are relative to
`components/continuity/`. Run Go checks from this component directory.

## Product contract

Build a local-first continuity and activity controller for coding agents. A fresh agent session should receive the smallest sufficient, attributable bootstrap needed to continue productive work without repeating discovery or failed attempts.

## MVP boundaries

- Go single binary and SQLite; no required daemon, container, cloud account, or local model.
- Codex and Claude Code lifecycle hooks are first-class integrations; MCP is the portable context interface.
- Record structured activity, checkpoints, attempts, verification, and usage. Do not treat transcripts as canonical state.
- Redact before persistence. Never capture stdin, credentials, or the entire environment.
- Do not expose generic command execution, SSH, deployment, or autonomous policy enforcement in the MVP.
- Procedure mining may propose and export human-reviewed recipes; it must not silently automate them.
- Preserve raw observations append-only. Corrections and interpretations supersede; they do not rewrite history.
- The History-sync memory repository is a special case with its own rules of engagement (ownership, commits, authentication): follow `docs/memory-repository.md`, not the code-repository rules.

## Engineering rules

- Keep client-specific payloads behind adapters and translate them into versioned canonical events.
- Use explicit SQL and migrations; avoid an ORM.
- Start Git only through `internal/gitenv`, which drops inherited variables that could point Git at another repository; a test rejects direct `exec.Command("git", ...)`.
- All generated bootstrap packets must have a token/size budget and provenance.
- Exact usage and estimated usage must never be combined without labeling confidence.
- Prefer deterministic parsing and verification over model inference.
- Add fixtures for every supported hook event and golden tests for bootstraps and reports.
- Run `go test ./...`, `go vet ./...`, and `go fmt ./...` before declaring work complete.

## Operational notes

- Hooks require `llewm` on PATH. The root `--project-root` flag binds CLI commands and an MCP process to one Git project. See `docs/installation.md`.
- Plugin source edits may need a plugin cache refresh and a new client thread; see `docs/client-integrations.md`.
- Bootstrap reads root project guidance, not nested component guidance such as this file; see `docs/global-guidance.md`.
- Use `docs/product-context.md` when a product or scope decision is needed; do not load it for routine mechanical changes. `docs/checkpoints.md` covers checkpoint persistence changes.
- Earlier handoffs and development trial logs are retained in original Git history and the private source archive; consult them only for a specific historical question.
