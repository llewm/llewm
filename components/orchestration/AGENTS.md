# Orchestration

This component owns an explicitly started team coordinator for manually claimed
work. It is independent of Continuity's database and internal packages. Consume
source context through explicit references and versioned snapshots.

- Authenticate engineers at the coordinator; machine and agent labels are
  caller assertions, never permission grants.
- Keep provider credentials and execution on the engineer's machine. This
  milestone must not launch agents, execute commands, or merge source changes.
- Reassignment fences publication authority, not offline processes. Retain late
  contributions with their original attempt and require explicit reconciliation.
- Persist redacted observations and immutable revision/attempt/submission history.
- Run `go fmt ./...`, `go test ./...`, and `go vet ./...` from this module.
