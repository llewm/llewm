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

`llewm update plan --source-root /path/to/llewm --output json` assembles a
read-only machine inventory. It reads the exact source HEAD's writer API and
migration filenames, each distinct registered store, and native client plugin
registrations at every observed scope. It prints backup-gated migration commands
only when a reachable checkout still routes to that store. The command never
fetches, installs, migrates, prunes, refreshes plugins, or restarts processes.

`--manifest FILE` selects a distribution target instead. Older manifests without
writer/schema fields remain supported; those target facts are unknown. With no
target flags, the configured source checkout is used when available, otherwise
the running executable is the comparison target. Cache digests are compared with
this executable's bundled plugin files, not an uninstalled candidate. Rerun the
plan with the qualified candidate before applying its migrations or plugins.

Missing, inaccessible, malformed, or newer registries/stores are reported without
creating a replacement. Registry inventory covers observed routes, not arbitrary
databases elsewhere on disk. Live MCP identities are not observable from this CLI:
compare `continuity_info` in each client after an update. A prospective floor rise
means older writers may defer checkpoints; it does not prove that one is running.

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
preserves dirty user work, reviews upstream evidence, builds into a separate candidate
path, and verifies the candidate identity before touching the installed binary.
It does not build unreviewed code merely because a remote branch changed. Follow
the common qualification flow below and retain the resulting installation receipt.

## Upstream verification evidence

Ordinary end-user installation/update, including a source build, does **not** run
the development test suite, vet or formatting checks. Development and CI retain
their required checks; an explicit request for local testing can also run them.
Installation uses upstream evidence plus local executable identity, PATH, doctor,
compatibility, backup/migration, plugin and fresh-client checks. No evidence gap
silently launches local tests, and a successful build is not a test pass.

The GitHub `check` workflow in `.github/workflows/check.yml` prepares one JSON
receipt per native Linux/Windows runner, using
`components/continuity/scripts/verify-upstream.py`. It records the actual checked
out commit, repository, OS/architecture, Go version, run/attempt URL and separate
test, vet, format and build results for all four Go modules, including standalone
module builds and Continuity's Python tests. Receipts are retained as workflow
artifacts for 90 days, subject to repository retention limits. The existing
Forgejo workflow remains independent; its older Ubuntu job is not Windows
evidence. Neither workflow qualifies interactive clients or untested platforms.
A successful test command can include individual skipped tests; inspect the run
logs for those limits rather than claiming every test executed.

Acquire evidence only from the installation's reviewed repository/channel. On
GitHub, use the [workflow-runs API](https://docs.github.com/en/rest/actions/workflow-runs)
to find `check.yml` runs with `head_sha=FULL_COMMIT`, then the
[run-artifacts API](https://docs.github.com/en/rest/actions/artifacts) to obtain
the matching `verification-...` artifact. Inspect the run's repository, event,
workflow revision, attempt and completed conclusion, as well as its native job
results. Prefer a push run on the reviewed upstream commit. A pull-request merge
run tests its synthetic merge commit, not necessarily the requested source HEAD.
Use existing read-only authentication if artifact download requires it; never
place credentials in receipt files, URLs or installation records. Downloads may
expire or require access even when run metadata is public. Do not dispatch or
rerun CI merely to install. A workflow file in source is not evidence of a run.

After obtaining and extracting the JSON from that run, compare it locally:

```sh
llewm update evidence --receipt verification.json --commit FULL_COMMIT --target windows/amd64 --output json
```

The default target is the running executable's OS/architecture. Use the actual
installation target, including `linux/...` for WSL. `--repository` selects a
reviewed fork instead of the default `https://github.com/llewm/llewm`. The command
reads local files only, creates no project state and runs no subprocesses. It
compares a full commit, never a branch/tag name or abbreviated hash. For source
installation also verify a clean checkout and the built candidate's stamped
identity; a receipt for an unmodified commit does not cover dirty source. Older
installed versions lacking `update evidence` can inspect the JSON directly or use
the staged candidate; do not install merely to read a receipt.

Version-1 receipts contain `schema_version`, `repository`, `commit`, `target`,
`go_version`, `run_url`, `status` and a `checks` array of `component`, `name`,
`status`. Results are `passed`, `failed`, `pending` (checks also allow `skipped`).
All required checks must pass; missing checks, duplicate entries, unknown schemas
or malformed identities cannot establish a pass. For a binary bundle the publisher
must additionally bind an `artifact` with `kind: binary-bundle`, `target`, `size`
and `sha256`; pass `--artifact BUNDLE` to compare its bytes. The current source CI
does not produce release bundles or bundle attestations. A manifest checksum alone
does not establish that a bundle was tested.

The command reports observations with exit code zero, including non-passing
states; automation must inspect `state`. Invalid command arguments return an
error. Interpret evidence separately from installation success:

| State | Meaning and installation handling |
| --- | --- |
| `passed` | The matching receipt reports all checks passed. Authenticate its origin/run separately; retain local installation checks. |
| `failed` | Upstream reports a failed check. Report the failure before replacing the installed version; use a known qualified revision or an explicitly reviewed exception. |
| `pending` | A run/check is unfinished or skipped. Never describe it as passed. |
| `missing` | No receipt/file, or no bundle digest for a requested bundle. Report upstream verification as unknown. |
| `unavailable` | Evidence cannot be read/retrieved (including offline, access or expired artifacts). Preserve offline/local-first installation and report the limit. |
| `incomplete`, `invalid`, `mismatch` | Evidence cannot qualify this selection. Explain the gap and do not infer a pass. |

Network lookup states are reported by the acquiring agent; the offline command
only classifies supplied files. When no receipt is available, omit `--receipt` to
record `missing`. Missing/unavailable evidence is not by itself a new installation
approval gate: honor the authorized source trust and installation scope, retain
local checks and report uncertainty. Never silently substitute local tests or
transfer a Linux result to Windows/macOS. `publisher_authentication` remains
`not_verified`: a local JSON file and a URL are claims, not signatures. Record the
reviewed run URL and acquisition provenance separately. A retained, previously
authenticated receipt can be used offline for its exact identity; its presence
does not establish latest-version discovery.

Adding the workflow does not assert that GitHub Actions is enabled or that any
particular commit passed. Until a matching completed run and receipt are observed,
report missing/pending evidence. Publishing releases, enabling infrastructure and
changing access remain separate from an installation request.

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
   platform, API/writer compatibility and expected migration target. Review
   [upstream verification evidence](#upstream-verification-evidence); source installs
   skip development tests by default. No candidate replaces a running binary
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
