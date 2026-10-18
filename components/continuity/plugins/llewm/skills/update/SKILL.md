---
name: update
description: Check or update the Llewm executable and matching client plugins, including verified source builds and backup-gated project migrations. Use for updating Llewm itself, not ordinary project dependencies or history sync.
---

# Update Llewm

Honor the requested scope: a check returns observations without installing.
An authorized update prepares and verifies a concrete candidate, preserves current
knowledge/configuration, applies required migrations explicitly and verifies the
running client. The executable's update checker performs no install or migration.

Read the matching `components/continuity/docs/updates.md` from the identified
source checkout or published revision at
https://github.com/llewm/llewm/blob/main/components/continuity/docs/updates.md.
Use only the relevant source-build or binary-bundle path and common qualification
steps. Do not scan all project docs or infer an installation from the target
project's Git remote.

First reuse a current-host installation resource or installer receipt: method,
source checkout/remote/branch or pinned release, installed executable, affected
stores, plugin owner and prior checks. Compare `llewm version --output json`,
actual MCP `continuity_info` and `llewm doctor --output json`. If installation
provenance is missing, inspect the identified executable/owner and ask for the
source location or channel once rather than hunting across disks.

For source builds, run `llewm update check --source-root CHECKOUT --output json`.
Use `--fetch --remote NAME --branch BRANCH` when checking the authorized upstream.
Preserve dirty source; prepare a separate clean checkout at the exact selected
commit rather than resetting user work. Read that revision's Go requirement and
checks. Build to a separate candidate path and verify its clean commit/API/writer
identity before replacing anything. A changed upstream is not permission to
build arbitrary code or change a pinned version; honor the selected source trust.

For release binaries, use actual publisher metadata and a matching platform
bundle. `llewm update check --manifest MANIFEST --artifact BUNDLE --output json`
checks identity/size/hash, not publisher authenticity or whether it is latest.
Verify provenance and published signatures separately. If no trustworthy released
artifact exists, report that and offer the source path. Preserve package-manager
ownership instead of overwriting its files with a manual install.

Follow the shared qualification steps in `updates.md`: save/read back a checkpoint,
inspect active writers and compatibility, retain verified DB/config/binary backups,
stage and verify the candidate, and run exact backup-gated migrations for each
affected existing store. Never initialize replacement storage or lower writer
floors to force an update. If a required handoff cannot safely stop this session's
own MCP, finish preparation and report the concrete restart step.

Refresh only matching plugins through their existing owner/marketplace. Treat new
permissions, trust, owner changes, format conversions and data deletion as separate
scope. Use existing explicit grants where applicable; do not create or renew one
yourself. Honor terminal-only steps without simulating confirmation. A routine
authorized update needs no repeated approval for already-covered steps.

For installer-owned packages, require the receipt's `plugin_cache.status=current`.
A stale Claude installation needs native `plugin update` at its existing scope;
repeating install can be a no-op. Source-owned plugins need matching manifest
version bumps when packaged files change. Verify cached bytes, not only versions.

Report installed, migrated, plugin-refreshed and live-verified separately. Restart
the parent IDE/client as needed, compare actual MCP identity and verify exact
checkpoint recovery in a fresh session. Require `restart_required=false` and
`plugin_status=current`; inspect `runtime.plugin` root, basis and digest. A Codex
cache-layout inference is not a client attestation, and disk verification does not
prove loaded skills or hook trust. Unknown plugin identity is a pending check.
Leave unobserved native-client checks
pending. Refresh the attributed current-host installation resource with exact
source/release identity, retained backup/migration receipts and remaining limits;
remove scratch artifacts. Do not repeat completed naming/layout/format migrations.
History sync remains separately scoped to its approved destination/profile.

After a restart has been observed, finish the saved qualification in the same
turn: verify runtime identity and exact checkpoint recovery, reconcile the
selected task's pending actions and bound plan, then save/read back completion.
Do not reinstall to satisfy a stale version-specific reminder; first check whether
a verified descendant and later rollout evidence satisfy it. A restart event
alone never proves client activation or semantic completion.
