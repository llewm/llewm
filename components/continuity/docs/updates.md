# Check and apply Llewm updates

The executable provides read-only update comparisons. The bundled `update` skill
guides an authorized agent through acquisition, qualification, backups, explicit
migrations, plugin refresh and fresh-client verification. There is no unattended
self-replacing updater or automatic migration from hooks/MCP startup.

Executable-only updates do not require a project in the current folder. Use the
existing installation's method, path and owner; do not initialize a replacement
store to qualify an executable. Identify any affected stores or active clients
from installation receipts and explicit paths. Apply the project backup/migration
and recovery steps below to those existing stores, even if this workspace has no
project. With no stores or integrations affected, retain a standalone installation
receipt and omit project checkpoint/migration/recovery steps. Configure a project
later through [guided onboarding](onboarding.md#choose-the-project-topology).

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

## Automatic availability reports

Guided source-install onboarding enables checks against the installation's
explicit source checkout, remote and branch, including fetch. It uses
`--if-unconfigured` to preserve existing settings and opt-outs. Onboarding reports
the opt-out command `llewm update configure --enabled=false`. Binary installations
stay off until an automatic release feed is available.

Outside guided source onboarding, checks remain disabled until configured. Enable
them once per engineer installation, using an explicit Llewm source checkout (not
the project currently using Llewm):

```sh
llewm update configure --enabled --source-root /absolute/path/to/llewm --interval 24h
llewm update configure --fetch --remote origin --branch main
llewm update status
llewm update configure --enabled=false
```

The first command compares local checkout HEAD. The second additionally opts in
to fetching the selected configured remote branch. Settings not supplied to
`configure` are preserved. Settings live in `update-check.json` under the global
Continuity configuration directory, alongside engineer guidance; they are not
synced project policy. `status` reads settings without performing a check.
`configure --if-unconfigured` publishes defaults only when no configuration exists;
it never replaces an existing file, including a disabled configuration.

Full-mode Codex and Claude Code SessionStart hooks check when the interval has
elapsed and append a timestamped result to the session context. Cached results,
including failures, are reused across projects until the next check is due.
Changing settings or installed build identity invalidates the cache. Simultaneous
session starts may perform duplicate checks; results are published atomically.
Nothing runs on a timer while no sessions start, in audit-only mode, or from
read-only MCP/bootstrap calls. Hook/plugin adoption still needs its usual setup.
When no configuration exists, full-mode SessionStart reports that update checks
are off and names the enable command, without fetching or creating settings.
An explicitly disabled configuration stays quiet.

The interval accepts `1m` through `720h`; `--timeout` accepts `100ms` through `10s`
and defaults to `3s`. Automatic Git checks disable interactive credential prompts;
timeouts and offline failures report `unavailable` without failing bootstrap
delivery. Checks never apply updates. A local checkout comparison does not
establish the latest upstream version; use explicit fetch for that remote/branch.
Binary manifests remain available through manual `update check`; there is no
automatic release feed yet.

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
4. Stage and verify the candidate, replace only the identified installed file
   (see [replacing a running executable](#replacing-a-running-executable)),
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

### Replacing a running executable

The client's MCP server and hooks keep the installed executable in use, so the
default is an in-session swap, finished by the restart in step 6. Closing the
client first is the exception.

- **Linux and macOS:** install the staged candidate with an atomic rename onto
  the installed path. A running process keeps its already-open image.
- **Windows:** copying over a running `llewm.exe` fails because the file is in
  use, but renaming it succeeds. Rename the installed file to its retained
  backup name, for example `llewm.exe.before-COMMIT-TIMESTAMP.bak`, then copy
  the verified candidate to the original path. Do both in one step: a hook that
  starts between them finds no executable. Remove an old backup only once no
  process still runs it. Deleting a running image fails, and a later cleanup can
  retry.

Until the restart, versions are mixed. Each hook and CLI call starts the new
executable, while the client's MCP server keeps running the old image, and
`continuity_info` reports `restart_required: true`. That is safe in both
compatibility cases:

- **Writer floor unchanged** (ordinary updates and additive migrations): both
  versions keep writing. Migrations with one-time hook work, such as the
  commit-attribution repair requests, run on the next hook.
- **Writer floor raised:** the old MCP server refuses record writes and keeps
  serving reads. A checkpoint returns `deferred: true` with a device-local
  recovery envelope instead of being lost. After the restart, run
  `llewm checkpoint recovery apply ID` and require a verified readback. Do not
  also save the checkpoint through the CLI, because that duplicates the staged
  handoff. See [migration-bearing updates](installation.md#migration-bearing-updates).

Use an offline maintenance handoff (a checkpoint with `resume_after_restart`,
then closing the client and running a terminal procedure) only when a step
cannot run while the client is open. Examples: a package or installer owner that
replaces files it cannot rename, or a migration whose documented procedure
requires stopping every writer. An "in use" copy failure on Windows is not
such a reason; use the rename swap above.

Invoking the update skill authorizes the requested update within the user's
stated scope; it does not create a standing grant for future updates, change plan
delegation, or authorize publication. Existing explicit grants can cover routine
agent-led updates and migrations. Other users need no knowledge of this project's
private development/test-box procedures. Availability checks run manually or
through the opt-in session-start setting above. Applying updates still requires
authorization.
