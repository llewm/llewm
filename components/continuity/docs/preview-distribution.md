# Preview distribution

Packaging produces local binary bundles, matching client packages, a provenance
manifest and SHA-256 checksums from a clean committed checkout. It does not
publish, install, migrate storage or activate reporting. Stable/public release,
source-publication and runtime platform qualification remain separate gates.

First verify dependency notices from the repository root (Go and Python 3):

```sh
python3 components/continuity/scripts/dependency-notices.py --check
```

From `components/continuity`, with Go and Git installed:

```sh
go run ./cmd/package-preview --version 0.1.0-preview.1 --targets all
```

Or from the root with Task installed:

```sh
task package-preview VERSION=0.1.0-preview.1
```

The default output is root `dist/previews/VERSION`. It must not already exist.
Failure removes staging files, which are kept beside the output on home disk,
not in `/tmp`. A custom `--output` must be outside the checkout or ignored.
Source changes during packaging refuse finalization.

Without `--targets`, the direct command builds the host platform. `all` builds
Linux, macOS and Windows on amd64 and arm64. A comma-separated list such as
`linux/amd64,windows/amd64` selects specific targets. Builds disable CGO and
embed VCS identity and explicit preview-version stamps.

Platform ZIPs contain `llewm`, the optional separate `llewm-report`
adapter (both `.exe` on Windows) and `INSTALL.txt`. Client ZIPs contain the
`llewm/` plugin root for Codex or Claude Code. Client manifests get
the same version without changing source manifests. Only tracked plugin files
ship; ignored local files are excluded, non-regular files and unsafe paths
are refused. The manifest records asset hashes/sizes, source commit and build
time. `SHA256SUMS` also covers the manifest. Checksums detect corruption, not
publisher authenticity.

## Adopting projects

Install once per machine and initialize each project's Git root separately:

```sh
llewm --project-root /path/to/project init
llewm --project-root /path/to/project doctor
llewm --project-root /path/to/project version
```

Follow [installation](installation.md) for persistent PATH setup and explicit
backup/migration of existing stores, then [client integration](client-integrations.md)
to register the matching local plugin and review hooks. Restart parent IDEs and
long-running clients. Distributing executables does not distribute project state.

Opt authorized own projects into the
[private Forgejo adapter](../../reporting/README.md) separately. Llewm's forge
receives selected product bugs; each project's memsync carries its own history.

Cross-compilation is a build check, not interactive console or client runtime
qualification. Linux is the current observed host. Native Windows and macOS
interactive qualification is still required.

Every platform bundle includes the root MIT `LICENSE`; plugin archives include it
under `llewm/LICENSE`. Binary bundles also include `THIRD_PARTY_NOTICES.txt` with upstream dependency
and Go runtime notices. Regenerate and review it after dependency changes; see
[source publication](../../../docs/source-publication.md#dependency-notices).
