# Installation

For an agent-guided setup and first-session verification, start with
[guided onboarding](onboarding.md).

For preview archives and matching client packages, see
[preview distribution](preview-distribution.md). These source-build instructions
remain useful for development and unqualified platforms.

Build from `components/continuity/` with Go and Git installed. From the Llewm
repository root, first run `cd components/continuity` in your shell. Use a Go toolchain
compatible with `go.mod`; see the [Go installation guide](https://go.dev/doc/install).
These instructions cover Linux, macOS, native Windows, and WSL. Linux has been
verified locally; macOS and Windows runtime and client integration remain unverified.

`llewm` is the canonical executable and plugin name. Continuity remains the internal
component name. Existing `.continuity/` stores, `CONTINUITY_*` environment variables,
and internal MCP tool names remain unchanged. Portable format 3 requires the
explicit history-preserving conversion described in [memory repository rules](memory-repository.md#format-3-conversion); it is separate from SQL migrations.
Install the executable on PATH so hooks and MCP launchers can resolve it.

When updating a pre-release installation, replace the old plugin with `llewm`,
review its hooks and regenerate client permission rules and shell completions.
Remove old `continuity`/`cuity` executables only after identifying them and confirming
they belong to this installation; no compatibility aliases are installed.

## Linux and macOS

Run in Bash or Zsh from the component directory:

```sh
mkdir -p "$HOME/.local/bin"
go build -buildvcs=true -o "$HOME/.local/bin/llewm" ./cmd/llewm
export PATH="$HOME/.local/bin:$PATH"
llewm --help
```

The `export` changes this shell and its future child processes. If this directory
is not already on your persistent `PATH`, add the following line once to your
shell startup file:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

- Bash: `~/.bashrc` for interactive terminals. Login shells read the first existing
  file among `~/.bash_profile`, `~/.bash_login`, and `~/.profile`; ensure that file
  also sets the path or sources `~/.bashrc`.
- Zsh (including the usual macOS terminal setup): `~/.zshrc` for interactive
  terminals; `~/.zprofile` if you also need it in login shells.
- Fish: instead of the `export` command, run `fish_add_path "$HOME/.local/bin"`
  once; Fish persists this setting.

See the [Bash startup rules](https://www.gnu.org/software/bash/manual/html_node/Bash-Startup-Files.html),
[Zsh startup rules](https://zsh.sourceforge.io/Intro/intro_3.html), and
[Fish PATH documentation](https://fishshell.com/docs/current/cmds/fish_add_path.html).

### Read-only Go build cache in sandboxed development

Some agent sandboxes can read the default user Go build cache but cannot write to
it. The symptom is an otherwise valid `go test`, `go vet`, or `go build` failing on
an `open .../.cache/go-build/...: read-only file system` error. This is an
environment failure, not a source or test failure.

Prefer fixing the Go environment once instead of spending model context rediscovering
and retrying the same workaround. Inspect the current user-wide setting with:

```sh
go env GOCACHE GOENV
```

Use Go's default `~/.cache/go-build`, which Go trims itself. Add that location
to the development sandbox's writable roots when needed. Keep caches on disk;
a small tmpfs such as `/tmp` can fill during builds. To remove a user-wide cache
override and restore Go's default selection, run:

```sh
go env -u GOCACHE
```

## Windows (PowerShell)

Run from the component directory. This installs into your user profile without
administrator privileges:

```powershell
$llewmBin = Join-Path $env:LOCALAPPDATA 'Programs\Llewm\bin'
New-Item -ItemType Directory -Path $llewmBin -Force | Out-Null
go build -buildvcs=true -o (Join-Path $llewmBin 'llewm.exe') ./cmd/llewm
if ($LASTEXITCODE -ne 0) { throw 'Continuity build failed' }
$env:Path = "$llewmBin;$env:Path"
llewm --help
```

Persist the directory in your **user** Path, preserving existing entries:

```powershell
$llewmUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($llewmUserPath -split ';') -notcontains $llewmBin) {
    $llewmUpdatedPath = (@($llewmUserPath, $llewmBin) |
        Where-Object { $_ }) -join ';'
    [Environment]::SetEnvironmentVariable('Path', $llewmUpdatedPath, 'User')
}
```

Alternatively, use Windows' **Edit environment variables for your account** dialog
to add that directory to the user Path. The session assignment above updates the
current terminal; the persistent setting is for new processes. See
[PowerShell environment variables](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_environment_variables).

## WSL and other execution environments

Inside WSL, follow the Linux instructions using a Linux Go toolchain. A Windows
installation does not replace an installation inside WSL. Install separately in
each environment that runs the client, including remote development hosts or
development containers. Other Unix-like platforms can use the Linux/macOS pattern
when the Go dependencies support them, but are not currently runtime-verified.

## Shell completion

`llewm completion <shell>` generates a completion script for Bash, Zsh,
Fish, or PowerShell, covering commands, flags, and known `checkpoint create
--status` values. The script is generated for whichever name you invoke it
as; generate it as `llewm` so it registers the canonical command. These commands
only print or write a script — they do not edit your shell startup files.

### Bash

Bash completion requires the `bash-completion` package, including when sourcing
scripts directly. Install it using your system's package manager and load its
initialization script before trying completion. A common Linux path is:

```sh
source /usr/share/bash-completion/bash_completion
```

Use your package's actual path if it differs. Ensure your `~/.bashrc` loads
that initialization script in new interactive shells; create `~/.bashrc` if
needed. Installing the package alone does not guarantee your shell loads it.

Try it for the current shell session:

```sh
source <(llewm completion bash)
```

To persist it with `bash-completion`'s default user directory:

```sh
llewm_completion_dir="${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions"
mkdir -p "$llewm_completion_dir"
llewm completion bash > "$llewm_completion_dir/llewm"
```

If you customized `BASH_COMPLETION_USER_DIR`, use a `completions` subdirectory
of that location instead. The package loads these files on demand when you
complete the command in a new shell.

Alternatively, add the `source <(...)` line above once to `~/.bashrc`,
after the package initialization.

### Zsh

Try it for the current shell session first:

```sh
autoload -Uz compinit
compinit
source <(llewm completion zsh)
```

To persist it, save the script under a directory on your `$fpath` using the
`_<name>` naming Zsh's completion system expects:

```sh
mkdir -p "$HOME/.zsh/completions"
llewm completion zsh > "$HOME/.zsh/completions/_llewm"
```

For a fresh setup, create `~/.zshrc` if needed and add:

```sh
fpath=("$HOME/.zsh/completions" $fpath)
autoload -Uz compinit
compinit
```

If your configuration already initializes completion, add only the `fpath`
line before its existing `compinit` call. Use `$ZDOTDIR/.zshrc` instead if you
set `ZDOTDIR`. Preserve Zsh's default `$fpath`: [`compinit`](https://zsh.sourceforge.io/Doc/Release/Completion-System.html) and its helper
functions come from the shell installation. If `$fpath` is empty or those
functions are missing, restore the installation's function paths first;
adding only the directory above is insufficient. Open a new shell after
saving the files.

### Fish

Try it for the current shell session first:

```fish
llewm completion fish | source
```

Fish loads a completion file automatically if its filename matches the
command name, so persist it with:

```fish
set -l llewm_completion_dir "$__fish_config_dir/completions"
mkdir -p "$llewm_completion_dir"
llewm completion fish > "$llewm_completion_dir/llewm.fish"
```

Use Fish's resolved `__fish_config_dir` so the destination matches its own
configuration directory, including unset, empty, or customized `XDG_CONFIG_HOME`.
Create the parent directory before redirecting; writing a script to a directory
outside Fish's search path does not activate it. Fish initializes its completion
search path from this same directory; see [Fish configuration source](https://github.com/fish-shell/fish-shell/blob/master/share/config.fish).

### PowerShell

Try it for the current session first:

```powershell
llewm completion powershell | Out-String | Invoke-Expression
```

To persist it, first create the current host's profile if it is missing:

```powershell
if (!(Test-Path -LiteralPath $PROFILE)) {
    New-Item -ItemType File -Path $PROFILE -Force | Out-Null
}
```

[`New-Item -Force`](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.management/new-item)
also creates missing parent directories. Keep the existence check
so an existing profile is not overwritten. Add the completion line
from the current-session example once to that file. Open a new PowerShell session in the same host to load it;
`-NoProfile` sessions do not load profiles.

### Keeping completion current

Completion scripts are generated from the binary's current command and flag
set. After rebuilding `llewm` with changes that add, rename,
or remove commands or flags, regenerate any saved completion files the same
way; a script saved from an older build keeps offering the older surface
until replaced. Sourcing directly from `~/.bashrc`/`~/.zshrc`/PowerShell's
`$PROFILE` (rather than saving a file) always reflects the currently
installed binary, at the cost of a small startup delay per new shell.

## Verify client access and update

Optionally run `llewm global init` to create engineer-wide rules, context,
and prompt guidance shared by all projects. See [Global guidance](global-guidance.md).

Restart terminals, clients, and parent IDE processes after changing `PATH`.
Desktop-launched applications may not read your shell startup files. If a client
cannot find `llewm`, launch it from a terminal where `llewm --help` works,
or configure its launch environment to include the installation directory.
Being on `PATH` means available to processes that inherit that path, not every
account or every already-running process on the machine.

From a Git project directory, verify the installation and initialize local state:

```sh
llewm --help
llewm init
llewm bootstrap
llewm work
```

`llewm init` is idempotent. Its output marks the configuration and database
as `created` or `existing`; when both already exist, it explicitly reports that
no initialization files were created. It does not run broader diagnostics
automatically. Run `llewm doctor` when you want to verify project discovery,
storage, schema, runtime identity, and client instruction files.

### Existing projects

Do not manufacture semantic history from every pre-installation commit. Treat
installation as an explicit adoption boundary and progressively retain the
facts, decisions, constraints, alternatives, verification, relationships, and
open questions that become relevant during real work. Record a first checkpoint
that names the current state, authoritative sources, and the fact that older
semantic history was not imported. Git remains available for deliberate source
inspection and code statistics; absence from Continuity before adoption does not
mean that work did not happen.

Existing attributable history is different from speculative reconstruction:

- bind or sync an existing Continuity lineage when this is another checkout of
  an already-known project;
- use `llewm usage ingest` separately when local usage telemetry is wanted;
- import only an explicitly selected, reviewed source. The Git-note importer is
  a narrow legacy source with a plan/hash/apply workflow, not a general promise
  that commit messages or repository contents are accepted project knowledge.

After research-heavy work, save the durable result rather than the raw search
stream. MCP API 108 checkpoint dry-run and save receipts include a best-effort research-capture
advisory when bounded activity shows substantial discovery. It compares counts
of already-redacted discovery operations with task-linked narratives, decisions,
work items, checkpoint evidence references, and structured checkpoint detail.
It never loads tool results, invokes a model, blocks a checkpoint, or claims to
judge semantic quality. See [Checkpoints](checkpoints.md#research-capture-advisory).

Then follow
[Client integrations](client-integrations.md) to enable the plugins, and add the
[client permissions](#client-permissions) Continuity needs. These commands
verify executable access and local state; they do not verify live hook activation.

When a shell, IDE, or MCP launcher cannot guarantee its working directory, pass
`--project-root PATH` before the subcommand. The path must exist and may name the
Git root or any path inside it. For example,
`llewm --project-root /path/to/project mcp` binds that server process to the
selected project for its lifetime. It does not permit per-call project switching;
run a separately bound process for another project. Omitting the flag preserves
current-directory Git-root discovery.

To update, build and verify the candidate at a separate path from the installed
executable. Stop clients/hooks and long-running MCP processes before replacing
the identified installed file. Follow the backup-gated workflow below when the
candidate contains pending migrations, then restart and verify the live client.
Updating the executable does not remove project state in `.continuity`.

### Migration-bearing updates

Ordinary commands, lifecycle hooks, and MCP startup never apply database migrations.
They inspect schema identity read-only and fail with `migration_required` when the
installed binary contains pending migrations. This means replacing the executable
cannot let the PostToolUse hook migrate the live database before a backup.

`llewm database status` reports the applied migration, the binary's compiled
target, and the exact pending list without changing storage. Only `llewm
database migrate` has migration authority. It requires exact `--from` and `--to`
confirmations plus a new `--backup` path, creates a consistent SQLite backup with
`VACUUM INTO`, verifies that backup, applies pending migrations, and then runs
database integrity and foreign-key checks. It refuses an existing backup path.
Passing migration tests on a temporary database does not mean an existing project
database has been upgraded.

For a verified migration-bearing release:

1. Stop long-running MCP processes so they cannot keep serving the old binary.
2. Build the candidate to a separate path, run its tests, and then install it. Do
   not build directly over the executable used by active hooks.
3. Run `llewm version --output json` and retain its commit, dirty flag, API version,
   and executable path with the verification record.
4. Run `llewm database status` and retain the exact applied and compiled
   migration names. Hooks may report `migration_required` until the next step; they
   will not write events or schema changes while the migration is pending.
5. Choose a new backup path and run the explicit operation using the exact names
   reported by status, for example:

   ```sh
   llewm database migrate \
     --from 026_procedure_monitoring.sql \
     --to 027_track_plans.sql \
     --backup .continuity/backups/continuity-before-027.db
   ```

6. Retain the JSON receipt. Confirm `backup_integrity` and `database_integrity` are
   `ok`, `foreign_key_violations` is zero, and `applied_migrations` is the expected
   list. Run `llewm database status` again and require `state: current`.
7. Restart clients and MCP processes so they launch the installed binary, then call
   MCP `continuity_info` and compare its commit/API/schema identity with the CLI
   result. Replacing the executable does not update an already-running process.
8. Call MCP `continuity_info` with `checks` with the required exact named probes (currently
   `narrative:<id>`), and optionally request the separately labeled integrity check.
   Treat `operationally_verified` as applying only to the supplied probe set.

After this initial restart, `continuity_info` detects later on-disk binary
replacement itself: `health.status: stale` and `restart_required: true` mean the
client must restart its MCP process. `unknown` means comparison could not be
completed and needs diagnosis; it is not proof that the process is stale. The
`runtime_warning` returned with MCP bootstrap makes confirmed staleness visible
without requiring a separate health call.

Stale processes also stop writing on their own, but only across a
write-incompatible upgrade. Such a build raises the store's writer floor; an
MCP server older than that floor refuses record writes while reads keep working.
A checkpoint request is the narrow exception: it returns `deferred: true` after
staging only the validated, redacted structured handoff in device-local recovery.

Restart, then run `llewm checkpoint recovery apply ID`; the command removes
the envelope only after confirmed save readback. List pending envelopes with
`llewm checkpoint recovery list`, and discard one only explicitly. Ordinary MCP API changes do
not raise the writer floor, so installing a tool-surface update does not strand
an active session before it can checkpoint. This keeps a long-running server
from writing with genuinely superseded semantics, such as incompatible
cross-task action handling, without treating every interface revision as a
storage-safety boundary. `llewm version` reports MCP and writer APIs;
`llewm database writer-floor` shows the floor. `--set N` lowers it only for
a deliberate rollback to an older writer.

### Database backups and retention

Create an integrity-checked SQLite snapshot without migrating the database:

```sh
llewm database backup
```

The command prints a JSON receipt with the database path, backup path, applied
migration, and integrity result. By default it writes a new timestamped file
under `.continuity/backups` and never overwrites an existing file. Use
`--backup PATH` for an explicit destination.

Configure the default directory and retention policy in `.continuity/config.toml`:

```toml
[backups]
dir = ".continuity/backups"
keep_last = 3
keep_days = 30
pinned = ["continuity-release-baseline.db"]
```

Retention keeps configured pins, the newest `keep_last` unpinned backups, and
backups no older than `keep_days`. Only regular `continuity-*.db` files in the
configured directory are managed; manually named files and non-regular entries
are protected. Relative pins are resolved inside that directory.

Preview pruning first:

```sh
llewm database backup prune
```

The preview is read-only and lists exact candidates and reclaimable bytes. Apply
that current policy explicitly with `llewm database backup prune --apply`.
Before each removal Continuity revalidates directory containment, file type,
size, and modification time, then prints a JSON receipt naming every removed
file. Migration receipts may include the same dry-run plan, but migration never
deletes backups automatically.

Review destructive or irreversible migrations and their recovery plan before this
sequence. An additive migration can still fail because of existing data, disk
space, permissions, or concurrent access; installation is not complete until the
live database and client-visible binary are verified.

## Client permissions

Coding-agent clients ask before running tools they have not been allowed, and
Claude Code's auto mode also runs a classifier that can block actions. Continuity
writes (checkpoints, action updates, narratives, plan revisions) are the user's own
bookkeeping, but a checkpoint quotes instructions and next actions, so a
classifier can mistake it for something riskier. An agent cannot grant itself
permissions, and should not: you add the rules.

`llewm client permissions` prints the rules for the current project:

- the Continuity MCP tools (`mcp__plugin_llewm_continuity` in Claude
  Code) and the `llewm` CLI;
- the commands of every procedure covered by an active standing grant (see
  [standing grants](resources-and-runbooks.md#standing-grants)), rendered from the
  procedure steps with the grant's parameter values, so an agent does not prompt
  for what you already granted; a step whose value is only known at run time
  becomes a client-specific prefix rule (review the unresolved-argument case below);
- a deny rule for `llewm standing-grant create`, so an agent cannot create
  a grant for itself (the command also refuses input that is not a terminal).
  Agents can still inspect its read-only help with
  `llewm help standing-grant create`; before recommending a grant they must
  verify the named accepted revision with `llewm procedure show PROCEDURE`.

For Claude Code, `--write` merges the rules into a settings file after backing it
up (`<file>.bak-<time>`), keeping your other settings and rules; rerunning adds
nothing. It needs a terminal, so run it yourself:

```sh
llewm client permissions --client claude-code --write --scope user
```

Scopes:

- `user`: `~/.claude/settings.json`, every project;
- `local`: `.claude/settings.local.json` in this project, personal and usually
  ignored by Git;
- `project`: `.claude/settings.json` in this project, shared with everyone who
  clones it.

The rules take this form:

```json
{
  "permissions": {
    "allow": [
      "mcp__plugin_llewm_continuity",
      "Bash(llewm *)"
    ],
    "deny": [
      "Bash(llewm standing-grant create *)"
    ]
  }
}
```

Grant-derived rules change when you create or revoke a grant, or when one lapses;
rerun the command then. The Claude merge only adds entries; it does not remove
rules from retired grants or commands. Review those separately.

Codex generation omits grant commands with unresolved parameters and reports
which procedure step needs binding. In particular, an unresolved remote shell
argument must never become a host-only SSH permission. Fully bound commands
still produce argv **prefix** rules; review the additional-argument scope.

Auto mode can still block a Continuity write after these rules are in place.
Continuity does not write auto-mode settings. If it happens, describe the
Continuity store as your own routine bookkeeping in your auto-mode settings; on
the development machine an `autoMode.allow` entry naming the llewm CLI and
the `plugin_llewm` MCP tools has worked since 2026-09-30.

For Codex, run this yourself at a terminal:

```sh
llewm client permissions --client codex --write
```

The command previews additions and removals, explains that approvals cover reads
and writes, then requires `yes`. It honors `CODEX_HOME` (default `~/.codex`),
writing `config.toml` and a dedicated `rules/llewm.rules`. These are client
approval rules, not filesystem or network sandbox permissions; they do not by
themselves fix a sandbox-denied memory-sync write or network connection.

Checksum-verified managed blocks are reconciled on subsequent runs: obsolete
managed tools and grant rules disappear, and unchanged runs write nothing.
Unrelated configuration and comments remain intact. Existing user-owned tool
tables (including `prompt`/`deny` choices and retired names) are reported and left
alone. Review older hand-added entries separately, including obsolete approvals
and unsafe SSH prefixes in other `.rules` files. An edited managed block or an
unmanaged `llewm.rules` file is refused for manual reconciliation; move
user-owned rules to another `.rules` file. Do not remove restrictions just to
make the generator run.

Changed existing files receive timestamped backups before either replacement.
Each replacement is atomic, ordinary partial failures roll back earlier writes,
and a process interruption leaves backups for manual restoration. Rerun if a
file changed after preview. Start a fresh Codex session after applying. The
standing-grant creation deny covers the displayed command spelling; effective
policy for root flags, absolute executable paths and shell wrappers requires
client-level testing. The CLI also independently refuses noninteractive grant
creation. Without `--write`, the command prints snippets without editing files.

`llewm doctor` reports a `client rules` line for each supported client it
finds installed, and `llewm doctor --readiness` lists a warning for each one missing
rules; clients that are not installed are not mentioned. Claude Code counts as
installed when `~/.claude` exists, and is set up when a settings file (user,
local, or project) allows both the Continuity MCP tools and the CLI. Codex counts
as installed when `$CODEX_HOME` (default `~/.codex`) exists, and is set up when a
rules file allows the `llewm` CLI and `config.toml` approves the Continuity
tools; `doctor` counts tools that are not approved. The plugin-scoped per-tool
structure matches the [OpenAI plugin documentation](https://developers.openai.com/plugins/build/plugins)
checked on 2026-10-04. The local check is still a presence heuristic, not proof
that effective policy, deny precedence, command variants, and grant-derived
rules work in the installed client. Missing rules are warnings, never failures.

## Version-pinned developer builds

The public module path is `github.com/llewm/llewm/components/continuity`.
Forgejo remains the source authority; GitHub is its reviewed source mirror. Until
a source revision is published there, build from the authorized local checkout.
For a reproducible build, check out the selected full commit in a separate clean
checkout, then run the platform's build command above. Record the commit and
`llewm version --output json` receipt. Keep binary and plugin source revisions
aligned.

Once the chosen commit is mirrored, Go can install each command separately,
using the same published full commit hash in place of `PUBLISHED_COMMIT`:

```sh
go install github.com/llewm/llewm/components/continuity/cmd/llewm@PUBLISHED_COMMIT
go install github.com/llewm/llewm/components/reporting/cmd/llewm-report@PUBLISHED_COMMIT
go install github.com/llewm/llewm/components/action-control/cmd/action-control@PUBLISHED_COMMIT
```

These are templates, not an assertion that a version is already published.
`go install` puts binaries in `GOBIN`, or `GOPATH/bin` by default; inspect
`go env GOBIN GOPATH` and add the resolved directory to PATH. Separate component
modules require separate invocations. The command installs the executable only;
install the matching client plugin from the same source revision or preview
bundle. Installing the reporting executable does not activate reporting.

For tagged versions, tags carry the module subdirectory prefix, for example
`components/continuity/v0.1.0-preview.1`; the package argument uses
`@v0.1.0-preview.1`. Do not offer `@latest` as a reproducible installation.
See the [Go module installation rules](https://go.dev/ref/mod#go-install) and
[source publication](../../../docs/source-publication.md).

Remote version-pinned installation still needs qualification after publication.
A Homebrew tap and signed prebuilt installers are pending; this guide does not
claim that either exists.

## Remove or disable an installation

Disable or uninstall the Llewm plugin in each client's supported plugin manager,
then stop its long-running MCP processes and restart the client. Review the
permission generator's managed entries and any older hand-added rules; preserve
unrelated approvals and restrictions. Do not delete an entire client config or
rules directory.

Remove only the identified `llewm` executable (and optional `llewm-report` or
`action-control` executables) from the installation directory. Remove the saved
completion file and PATH/startup line only if they were added for this install
and no other program needs that directory. Go's module/build caches are shared
and do not need removal.

Project `.continuity/` stores, backups, global guidance, and History-sync memory
are retained. Removing the executable does not delete project knowledge. Review
and back up data before any separately requested data deletion.

### Coordinated naming cutover

Before replacing a pre-release binary, preserve the existing global guidance and
configure an explicit path to its current memory clone. New default namespaces
refuse an existing `llmem` location rather than silently using an empty store.
See [existing global locations](global-guidance.md#existing-pre-release-locations)
and [format-3 conversion](memory-repository.md#format-3-conversion).

Stop or retire all old writers first. Verify a database backup and the candidate
identity, install the exact reviewed build/plugin pair, and set the database writer
floor to the candidate writer API after verifying the upgrade. Run the explicit
sync converter only when authorized; then verify idempotence, settled forks and
fresh-machine recovery. No code-history squash or memory-repository reset is
needed to change the product's transport name.
