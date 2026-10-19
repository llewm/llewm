<!-- continuity:roadmap-node continuity-operational-readiness -->
# Operational readiness states

Continuity distinguishes source completion from an installed, migrated, restarted,
and surface-qualified runtime. A single
`completed` label is insufficient for migration-bearing or client-served work.

## Readiness model

Roadmap status continues to describe product meaning: proposed, accepted, active,
blocked, completed, deferred, or superseded. Operational readiness is an orthogonal
deterministic view. Completing an implementation node does not assert that a user
installation was replaced, a live database was migrated, or a long-running MCP
process was restarted.

The readiness report keeps four axes separate:

1. **Source:** repository commit and dirty state, expected API, expected latest
   migration, and source verification claims.
2. **Installed artifact:** executable path, commit, dirty flag, API, and content
   hash compared with the intended source build.
3. **Database:** configured database identity, applied latest migration, pending or
   ahead state, and separately observed integrity result.
4. **Runtime:** process start, executable hash, commit/API, installed-artifact
   comparison, restart requirement, and separately qualified feature surfaces.

Each observation preserves its source and authority. Temporary-database migration
tests establish source behavior only. They never imply that an existing project
database was upgraded. Likewise, replacing an executable does not update a process
that already loaded the old binary.

## Derived readiness

Readiness states are:

- `source_only`: implementation and source checks exist, but no matching installed
  artifact was observed;
- `installed_migration_pending`: the intended artifact is installed but the active
  project database has not applied its expected migration;
- `migrated_restart_pending`: artifact and database match, but a long-running client
  still serves an older executable or API;
- `identity_current`: installed artifact, database schema, and fresh runtime identity
  match, but attributable source verification or feature-surface qualification was
  not supplied;
- `plugin_verification_pending`: the executable is current but MCP startup plugin
  files or launch identity remain unverified. Refresh through the existing owner,
  restart and inspect `continuity_info.runtime.plugin`; disk verification does not
  prove loaded skills or hook trust.
- `operationally_verified`: identity is current and the named required surface probes
  passed with attributable evidence;
- `unknown`: one or more required observations are unavailable; and
- `inconsistent`: the database is ahead, identities conflict, or another impossible
  combination requires diagnosis.

Readiness is computed from evidence; it is not a mutable optimistic flag. A result
names every unmet gate. `health.current` means installed and runtime identities
match each other, not necessarily that they match repository HEAD or that a feature
probe passed.

## Read the report

The implemented CLI surface is `llewm doctor --readiness`. It reports a missing MCP
runtime as `unknown`, because a standalone CLI process is not evidence about a
long-running client. Repeatable `--probe narrative:<id>` flags resolve only exact
current work narratives through the live service; unsupported probe kinds fail
closed. `--source-verification` preserves caller-reported check references without
relabelling them as machine observations, and `--check-integrity` runs and labels a
current `PRAGMA integrity_check` separately. MCP `continuity_info` with `checks` uses the
server's captured process identity and accepts the equivalent bounded inputs. The
report performs no generic command execution and persists no readiness flag.

## Completion language

Handoffs and roadmap reports should qualify completion precisely:

- `implemented`: source exists;
- `source-verified`: required source checks passed;
- `installed`: the intended artifact is present;
- `migrated`: the named project database applied the expected schema;
- `runtime-current`: a fresh process serves the intended artifact/API; and
- `operationally-verified`: required live probes passed.

Writers must not shorten `source-verified` to “finished operationally.” When a user
has already agreed to restart a client, report `migrated_restart_pending` and make
that restart verification the exact next action.

## Qualification

Qualification covers source-ahead-of-install, installed migration pending,
migrated stale runtime, clean current identity without probes, operationally
verified identity with probes, dirty source, database-ahead inconsistency, missing
runtime, and unavailable executable metadata. Fixtures must prove that source tests
cannot produce `operationally_verified` and that restarting is recommended only
when an actually observed long-running runtime is stale.
