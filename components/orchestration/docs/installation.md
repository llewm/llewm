# Install and update orchestration

`llewm-orchestrate` is a separate executable in the Llewm repository. Root
`task build` builds it alongside `llewm` and `action-control`; outputs in `dist/`
have the `.exe` suffix on Windows. CI checks the orchestration module as well as
Continuity. Building does not install executables on PATH or start a service.

Include orchestration when onboarding a user who requests coordination, and in
upgrades of an existing orchestration installation. Continuity-only and audit-only
installations do not require it. Installing the executable does not authorize
starting a broker, creating credentials, registering hooks, or exposing a listener.

## Source installation

Use the same selected, qualified source revision as the other Llewm components,
unless the installation receipt explicitly pins orchestration separately. Respect
an existing installation's directory and package owner. Do not assume a Continuity
preview archive contains orchestration; inspect its manifest. Source builds need
the toolchain in `components/orchestration/go.mod` and the sibling `privatestorage`
module. Ordinary installations use upstream verification evidence; they do not
implicitly run the development test suite.

For a new Linux/macOS installation, run from `components/orchestration`:

```sh
mkdir -p "$HOME/.local/bin"
go build -buildvcs=true -o "$HOME/.local/bin/llewm-orchestrate" ./cmd/llewm-orchestrate
export PATH="$HOME/.local/bin:$PATH"
command -v llewm-orchestrate
llewm-orchestrate --help
go version -m "$HOME/.local/bin/llewm-orchestrate"
```

For a new native Windows installation, from the same component directory:

```powershell
$llewmBin = Join-Path $env:LOCALAPPDATA 'Programs\Llewm\bin'
New-Item -ItemType Directory -Path $llewmBin -Force | Out-Null
$orchestrateBin = Join-Path $llewmBin 'llewm-orchestrate.exe'
go build -buildvcs=true -o $orchestrateBin ./cmd/llewm-orchestrate
if ($LASTEXITCODE -ne 0) { throw 'Orchestration build failed' }
$env:Path = "$llewmBin;$env:Path"
Get-Command llewm-orchestrate
llewm-orchestrate --help
go version -m $orchestrateBin
Get-FileHash -LiteralPath $orchestrateBin -Algorithm SHA256
```

Use the [persistent PATH instructions](../../continuity/docs/installation.md)
for the actual client environment. Verify the bare command there, not only an
absolute invocation in a developer shell. Do not leave a versioned `dist/`
executable as the only discoverable installation. WSL and remote hosts need their
own native executable. `go version -m` supplies revision and `vcs.modified`;
orchestration does not implement Continuity's `version` command.

## Upgrade an existing installation

1. Inventory the installed CLI, running server, MCP adapters and hook commands.
   Record paths, hashes/build revisions, database paths, endpoint, credential-file
   references and installation owner. Never record token contents. A PATH copy
   does not update a process or a hook pinned to another executable.
2. Build a separate candidate from the selected clean revision. Verify build
   metadata, SHA-256 and help before replacement. Retain the prior binary and
   configuration for recovery. Preserve explicit component pins.
3. Review the target's storage compatibility. Orchestration stores are independent
   of Continuity: `llewm database backup/migrate` does not cover them. Before an
   authorized service cutover or storage change, stop its writers and make and
   verify a consistent backup including any SQLite sidecars. Never copy a live
   database or claim an old binary can read a newer format without evidence.
4. Replace through the existing installation owner. On Windows, do not overwrite
   a running image; use a coordinated stop/swap/restart or the reviewed rename-swap
   pattern. Preserve the old image while processes still use it. Update existing
   hook/adapter paths that point into `dist/` to the installed absolute path,
   preserving endpoint, token paths, session binding, cursors, permissions and trust.
5. Restart affected services/adapters within the authorized maintenance scope.
   Retain broker identity, database, credentials and session/task/filter-scoped
   inbox cursors. Never reinitialize an existing broker to make an upgrade pass.
   Verify installed and running identities separately; an old running image is a
   pending activation check even when the new CLI works.
6. For a local broker, run `broker-info` and `broker-pull` with the configured
   endpoint/token file, actual session ID and its own saved inbox cursor. Confirm
   broker identity. Hook notification cursors are not agent inbox cursors. Verify
   hook delivery and adapter access in affected clients; report unobserved checks.
   Read-only verification does not send messages or acknowledgments. Shared
   services use their corresponding authenticated commands and existing TLS.
7. Update the installation receipt/resource with candidate, installed and running
   identities, backup/recovery paths, PATH resolution, client verification and
   remaining restart work. Remove scratch candidates after verification, retaining
   intended rollback artifacts. A copy is not automatically kept current.

For rollback, use the preserved configuration and compatible binary/data pair.
Do not restore stale data over newer messages without an explicit recovery
decision. A broker upgrade never implies a Continuity database migration.

For initial configuration, follow [local agent broker](agent-broker.md),
[shared broker](shared-agent-broker.md), or the [work coordinator](../README.md)
for the selected capability. Keep configuration and startup explicitly scoped.
