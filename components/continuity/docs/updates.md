# Check and apply Llewm updates

The executable provides read-only update comparisons. The bundled `update` skill
guides an authorized agent through acquisition, qualification, backups, explicit
migrations, plugin refresh and fresh-client verification. There is no unattended
self-replacing updater or automatic migration from hooks/MCP startup.

## Source builds available today

Install Git and a Go toolchain matching the target revision's `go.mod`. Use an
identified Llewm source checkout, separate from the project using Llewm:

```sh
llewm update check --source-root /path/to/llewm --output json
llewm update check --source-root /path/to/llewm --fetch --remote origin --branch main --output json
```

The first command compares the installed build with checkout HEAD without network
access. The second explicitly fetches that configured remote branch and compares
against its exact fetched commit; it never checks out, merges, resets or builds.
The remote/branch selection is explicit, not inferred from the target project.
`update_available` means the installed commit is a proved ancestor of the target.
`installed_ahead`, `diverged_or_history_unavailable`, unknown installed identity,
and dirty source are reported rather than silently choosing a downgrade/reset.
A shallow clone may need deliberate history expansion to establish ancestry.

Invoke `$update` in Codex or `/llewm:update` in Claude Code, or ask the agent to
update Llewm. Onboarding records the installation method, source checkout/remote,
branch or pinned revision, installed path and plugin ownership as an attributed
installation resource for the current host. Reuse that evidence on later updates;
if it is absent, ask for the source location once rather than hunting for it.
The check command itself needs no initialized project database or model.

The agent prepares a separate clean checkout of the exact selected commit,
preserves dirty user work, runs required checks, builds into a separate candidate
path, and verifies the candidate identity before touching the installed binary.
It does not build unreviewed code merely because a remote branch changed. Follow
the common qualification flow below and retain the resulting installation receipt.

## Published binary bundles

For a future published release, obtain its actual distribution manifest and
matching platform bundle from the reviewed publisher/channel. The existing
preview packager emits this manifest format:

```sh
llewm update check --manifest /path/to/manifest.json --artifact /path/to/platform.zip --output json
```

The checker selects the host OS/architecture, or an explicit `--target`, verifies
asset size and SHA-256 when an artifact is supplied, and compares commit identity.
`different_target` does not assert that an arbitrary manifest is newer or is the
latest release. Publisher authentication remains `not_verified`: a checksum
supplied alongside an archive detects corruption but does not authenticate it.
Verify publisher provenance and any published signatures separately. Reject an
unavailable platform or missing trustworthy release metadata; do not fabricate a
download URL, signature, stable release or qualification claim.

Public release binaries and an authenticated latest-release feed are not yet
available as an established installation channel. Until those exist, the source
flow is the usable default. The agent can retrieve a published manifest later;
the CLI deliberately does not poll an invented release service. This manifest
comparison also works with locally generated preview bundles today.

## Shared qualification and migration flow

1. Identify the installation method, exact installed executable, target source
   commit/release, client plugin ownership and affected project stores. Keep
   package-manager-owned installations under their manager's update mechanism.
   Respect pinned versions; a check is not permission to move a pin.
2. Verify a candidate separately: clean build identity, artifact provenance,
   platform, API/writer compatibility and expected migration target. Source builds
   run the target's documented checks. No candidate replaces a running binary
   while it is being built.
3. Save/read back a checkpoint; preserve previous binary, installation/client
   receipts and configuration. Back up each affected store using the supported
   command. Identify active writers and coordinate incompatible changes across
   machines. Do not terminate the agent's own MCP without a supported handoff.
4. Stage and verify the candidate, replace only the identified installed file,
   inspect each store with `database status`, then use the exact
   [backup-gated migration](installation.md#migration-bearing-updates) workflow
   where required. Never turn an update into `init` on replacement storage.
5. Refresh matching plugins through their existing owner/marketplace. Preserve
   permissions and trust; prepare a separate review if either needs expansion.
   For installer-owned packages, rerun `llewm install --client CLIENT` and require
   `plugin_cache.status=current`. Existing Claude installations use native update,
   not a repeat install. For source-owned plugins, bump matching manifest versions
   when packaged files change and refresh through the existing registration.
   Compare actual cached skills/hooks/configuration; a version string alone does
   not establish freshness. Regenerate saved completions when the command surface changes.
6. Restart clients/parent IDE as needed; compare actual MCP `continuity_info`
   with installed CLI identity and verify checkpoint recovery in a fresh session.
   Require `restart_required=false` and `plugin_status=current`; inspect the
   startup plugin root/basis/digest separately from executable identity. Unknown
   plugin identity requires investigation, not a completion claim. Disk identity
   does not prove session skill activation; verify fresh bootstrap and recovery.
   Report installed, migrated, plugin-refreshed and live-verified separately.
7. Retain verified backups and receipts; remove scratch artifacts. After new
   writes or sync, restoring an old database is a separate reconciliation decision,
   not an automatic rollback. Sync uses its separately authorized destination/profile.

Invoking the update skill authorizes the requested update within the user's
stated scope; it does not create a standing grant for future updates, change plan
delegation, or authorize publication. Existing explicit grants can cover routine
agent-led updates and migrations. Other users need no knowledge of this project's
private development/test-box procedures. Installation checks run only on explicit
request; recurring checks/automatic application require their own opt-in design.
