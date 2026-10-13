# Reporting adapter

This component delivers explicitly selected Continuity bug reports to a private
Forgejo issue tracker. It is an out-of-process adapter, independent of Continuity
storage and lifecycle hooks. Consume the versioned JSON report interface; never
open a Continuity database or import its internal packages.

- Require an explicit target, automatic-submission opt-in and source-project allowlist.
- Keep credentials in the named environment variable; never persist or print them.
- Redact selected report fields before writing the local outbox.
- Fail closed if the target is public, archived, has no issue tracker or redirects.
- Preserve pending reports across delivery failures and record attributable receipts.
- Exact source/report markers support retry recovery; they are not semantic duplicate detection.
- No daemon, model, shell execution, repository changes or generic network proxy.
- Run `go fmt ./...`, `go test ./...` and `go vet ./...` from this module.
