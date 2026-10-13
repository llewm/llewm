# Action Control

Action Control is a standalone Llewm component for deterministic validation of
explicit action proposals. Its first slice validates Git commit messages against
a versioned machine-readable policy. It does not import Continuity internals,
mutate Continuity storage, install hooks, or execute Git commands.

Run from `components/action-control/`, with an explicitly selected policy and
message file:

```sh
go run ./cmd/action-control validate-commit \
  --policy /path/to/policy.json --message /path/to/commit-message.txt
```

For Git's pending message, use the path reported by `git rev-parse --git-path
COMMIT_EDITMSG` in the target repository. A component directory need not contain
`.git`, and linked worktrees may store Git metadata elsewhere. This validator
does not install a `commit-msg` hook.

Use `--json` for stable structured output. A valid message exits 0, a policy
violation exits 1, and invalid input or I/O failure exits 2. Inputs are limited to
1 MiB each. The validator inspects Git's final trailer paragraph, ignores default
`#` commit-template comments without losing diagnostic line numbers, and matches
prohibited trailer keys case-insensitively.

The policy JSON contract accepts the ActionPolicy object returned by Continuity.
Schema version 1 supports `action_type: git_commit`, an explicit policy version,
an effect of `allow`, `deny`, or `proposal_only`, prohibited trailer keys, and a
Git-note requirement. When a Continuity canonical content hash is present, the
validator recomputes and verifies it before inspecting the message. Results retain
the policy hash and authority for downstream attribution. The validator reports the note requirement for a future
reviewed execution flow; a commit-message check cannot prove that a post-commit
Git note will be attached.

The validator is a bounded proof of concept. An opt-in hook adapter or reviewed
execution would require a concrete enforcement need and separately scoped work;
neither is part of this release.
