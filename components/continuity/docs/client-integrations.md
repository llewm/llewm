# Client integrations

These instructions install the full client integration. For an audit-first
baseline, use [audit-first onboarding](onboarding.md#start-with-usage-auditing)
instead; offline collection needs no plugin, MCP server or skills. Existing users
can follow the [audit-only switching checklist](usage.md#switch-an-existing-setup-to-audit-only).

Install the [Llewm executable](installation.md) on PATH. For requested project
setup, first [select the topology](onboarding.md#choose-the-project-topology),
then run these commands from the resolved project/unified workspace root, or each
selected federated member. `init` initializes project state;
`install` installs the bundled matching plugin at **user scope** for one client.
Run installation yourself at a terminal.

For explicitly requested plugin registration before a project exists, preview
`llewm install --client CLIENT --dry-run`, then run `llewm install --client CLIENT`
without `init` or `--permissions`. Defer project permissions, bootstrap and recovery
verification until project setup. Executable-only installation needs no plugin.

## Codex

```sh
llewm init
llewm install --client codex --dry-run
llewm install --client codex --permissions
```

Review the permission changes when prompted. Review and trust the installed hooks
in the client; use the CLI's `/hooks` review if your IDE does not expose one.
Plugin installation does not approve hook trust. See
[Codex trust and delivery details](client-integration-reference.md#codex).

## Claude Code

```sh
llewm init
llewm install --client claude-code --dry-run
llewm install --client claude-code --permissions
```

`--permissions` reuses the existing [permission setup](installation.md#client-permissions),
including backups and preservation of unrelated settings. Inspect the generated
rules before applying them. Client hook review and sandbox access are separate
from these approvals.

## Verify the fresh client

Restart the client and its parent IDE, then start a fresh session in the project.
Compare MCP `continuity_info` with `llewm version --output json`. Verify bootstrap
arrival, a first checkpoint's save/readback, and recovery in another fresh session.
The installer reports plugin registration, permission setup, hook review and
restart status separately; `live_verified` remains false until you observe the
fresh-client checks. See [guided onboarding](onboarding.md).

Hooks and MCP invoke `llewm` through PATH. Desktop clients must inherit the same
PATH and any `CONTINUITY_GLOBAL_HOME` override. A shell alias is insufficient.
The installer refuses a shadowed executable. See [PATH setup](installation.md).

## Updates and existing installations

After installing a new executable, stop client hooks and long-running MCP
processes, then rerun `llewm install --client CLIENT`. Run `--permissions` again
when the permission interface changes. Bundled content determines the installed
plugin version, including skills, hooks, launch configuration and generated
marketplace catalogs. Every changed input gets a new cache identity. Identical
repeat runs avoid plugin-manager writes. Installer-owned prior packages are
retained as backups; project stores and private memory are outside the package.

The installer uses Claude's `plugin update --scope user` for an existing stale
plugin and `plugin enable` only when disabled. Codex refreshes through `plugin add`.
Before reporting installation success, it compares cached files with the bundled
payload; Claude's direct-load folder is checked too. A matching registered version
alone is insufficient. A cache that remains stale after the native update produces
a partial receipt with its exact path; review that plugin through its owner rather
than deleting a client's entire cache.

`continuity_info.runtime.plugin` retains the plugin file observation from MCP
startup: client, version, root, root basis and content digest. The top-level
`plugin_status` compares that snapshot with the bundled payload and current disk
files. `current` verifies these files, not that the model has reloaded skills or
that hooks are trusted. A fresh session and exact checkpoint recovery remain
required. Changed files or a stale launch version require plugin refresh/restart;
missing launch metadata reports `unknown`, never a successful plugin check.

Claude supplies its expanded plugin root in the launch environment. Codex's legacy
MCP format does not interpolate a plugin-root variable, so Llewm inspects the
native `CODEX_HOME/plugins/cache/llewm/llewm/VERSION` path (default `~/.codex`) and
labels the basis `codex_version_cache_layout`. This is an inferred cache location,
not a client attestation. Custom layouts that cannot be observed remain unknown.
Source-owned plugins still require a manifest version bump when packaged files
change; the installer digest applies to installer-owned packages only.

Database migrations remain explicit and [backup-gated](installation.md#migration-bearing-updates).
The installer never migrates a database or configures History sync.

An existing `llewm` marketplace pointing at a source checkout is a different
installation. The installer reports the conflict and preserves it. Review its
scope and any enabled plugin, then remove that registration using the client's
plugin manager before switching to bundled setup. Do not delete an entire client
configuration or plugin cache. Source-development workflows remain documented in
[the integration reference](client-integration-reference.md#claude-code).

If a client command fails, the receipt names the partial state. The staged package
and previous-package backup are retained; correct the failure and rerun.
Unsupported client interfaces fail before package or configuration writes.
A stale installer lock requires checking for another installer before removing
that exact lock file. Modified, extra, or symlinked package files require manual
review; they are never silently overwritten.

## Scope and qualification

Without `--permissions`, plugin installation works outside a Git project and
leaves approvals for a separate reviewed step. `--permissions` needs an initialized
Git project. Use `--output json` for a receipt; permission-review text is sent to
stderr. JSON receipts do not contain raw client settings or plugin-list dumps.

The installer supports the installed clients' documented CLI interfaces, not
arbitrary commands. Isolated tests establish preview, ownership, repeat-run and
failure behavior. Native Windows/macOS and fresh-client activation remain separate
qualification gates; a successful install receipt does not establish them.

For protocol contracts, optional reminders/scans, skill behavior, troubleshooting,
and qualification matrices, see the [client integration reference](client-integration-reference.md)
and [hook conformance](client-hook-conformance.md).
