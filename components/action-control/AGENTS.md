# Action control component

These instructions supplement the repository-root `AGENTS.md`.

## Component boundary

- Own explicit action validation and, in later reviewed slices, narrowly scoped execution.
- Do not import Continuity internal packages or mutate Continuity's SQLite database.
- Start with standalone Git commit-message validation against an explicit versioned policy.
- Do not expose generic command execution, install hooks, or execute Git actions in this slice.
- Keep policy parsing deterministic and report precise, reviewable diagnostics.
- Run `go fmt ./...`, `go test ./...`, and `go vet ./...` from this module before completion.
