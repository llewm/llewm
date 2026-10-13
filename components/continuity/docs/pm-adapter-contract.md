# Read-only project-management interchange

Version 1 implements the first local, on-demand boundary from the
[PM integration design](roadmap-provenance-and-coordination.md#provider-neutral-project-management-integration).
The [JSON schema](pm-adapter-v1.schema.json) describes input shapes. The inspector's
semantic rules below are also required for conformance. This is a foreign
observation packet, separate from portable canonical Continuity records.

Run `llewm pm inspect --file packet.json --provider example --instance tenant
--project project` (on one line). The identity flags express the independently
expected external scope. `--project-root` is unused by this command; no Git repo,
Continuity configuration or database is opened. Success emits JSON and exits zero;
invalid input emits a static error to stderr and exits nonzero with no stdout.
The command reads only the explicitly supplied regular file and rejects symlinks.

An adapter prepares one UTF-8 JSON object containing `schema_version`, `manifest`,
`project`, `snapshot`, and `proposals`. All listed fields are required, including
empty arrays, empty `fields` objects, and boolean values. Only reference `url` and
snapshot `cursor` are optional. Null values, duplicate JSON keys, unknown structural
fields, and trailing JSON are rejected. Opaque provider fields may contain bounded
objects, arrays, strings, numbers and booleans; nulls must be omitted by the adapter.

The consumer separately supplies the expected provider/instance/project tuple.
Each reference and manifest must agree with it. These are provider identity labels,
not URLs or local Continuity project IDs. IDs use ASCII letters, digits and
`._:/@-`, start with a letter or digit, and have at most 256 characters. Adapters
must supply stable namespaced resource IDs unique across all types in the packet.
They must not embed credentials in identifiers or payloads.

The manifest identifies adapter and mapping-profile versions and declares exactly
contract version `[1]`. The supported capabilities are `read_snapshot` and
`propose_import`; a packet with proposals requires both. Resource types are
`work_item`, `initiative`, `milestone`, `research`, and `release`. Additional
capabilities/versions are rejected rather than silently negotiated. This v1 profile
does not advertise authentication, provider-schema compatibility or apply support.

Snapshots contain resource observations and relationships. Every reference has a
nonblank opaque revision and RFC3339 observation time. URLs, when present, are
HTTP(S). Relations use roadmap relationship vocabulary, reference present resource
IDs, and cannot be self-links or duplicate triples. Cycles are retained observations;
the inspector does not adopt them into the roadmap or establish their validity there.

`complete: false` always reports incomplete coverage, with or without a next-page
cursor. The inspector does not fetch or merge pages. A complete snapshot cannot
have a nonempty cursor. Absence never implies deletion. `deleted: true` records a
provider tombstone only. Conflicting observations of the same ID are rejected;
the caller must resolve them or submit separate snapshots.

Proposals reference a present, nondeleted resource and its exact observed revision.
There is at most one proposal per source, with a unique proposal ID. Each mapping
has a field, string value, declared authority (`provider`, `llewm`, or `neither`),
and explicit `lossy` boolean. Authority is an unreviewed adapter assertion.
Supported fields are title, status, kind, parent, dependency, release, and
target_date. Each appears at most once. Status/kind follow Continuity roadmap
vocabulary; parent/dependency values reference other resources in this snapshot;
target dates use YYYY-MM-DD. Release is an opaque label, not a release publication.
The small initial profile represents one parent/dependency per proposal; richer
relationship observations belong in the snapshot.

The byte limit is 1 MiB per input, with at most 256 resources, 512 relationships,
256 proposals, seven mappings per proposal, and 16 KiB UTF-8 per decoded string or
key. The root value is depth zero and no value may exceed depth 16. JSON Schema
character counts are a coarse bound; runtime byte, depth, identity, date, revision,
capability and cross-reference checks remain authoritative.

Inspection produces schema version 1, `state: valid`, a sanitized packet, stable
path-addressed diagnostic codes and a SHA-256 of the sanitized normalized packet.
Resources/proposals/mappings and set-like manifest/relationship lists have stable
ordering. Opaque array order is preserved and opaque numbers retain precision.
The hash identifies the sanitized view, not the original provider bytes, and is
neither an authorization token nor an import/apply plan. Input observation times
remain source assertions; the inspector adds no current timestamp.

Known credential fields and text patterns are redacted using Continuity's shared
redactor. All recognized HTTP(S) URLs lose userinfo, query and fragment, including
URLs in opaque data. Keys are sanitized too; a sanitized-key collision withholds
the entire affected object. Diagnostic paths never contain provider keys or
values; parse errors do not echo input. Redaction is pattern-based, not a guarantee
against arbitrary unlabeled secrets. Adapters must redact before producing files.
No original-input hash is emitted.

Opaque fields, lossy mappings, unresolved authority, incomplete snapshots and
tombstones are visible diagnostic codes. Rejected inputs produce static error
codes and no packet. Passing inspection makes no claim about local append/update/
no-op outcomes, reviewed field ownership, provider truth, write authorization,
import/export, retries, partial failures or reconciliation. Those require later
contracts and separate authorization.

## Adapter author conformance kit

The public examples are in [testdata/pm-adapter](../testdata/pm-adapter):
`minimal.json` is an empty complete snapshot; `paginated.json` includes a tombstone,
opaque fields, a lossy proposal and synthetic credential markers; `duplicate-key.json`
must fail. `cases.json` records expected outcomes and ordered diagnostic codes.
The synthetic `DO_NOT_PRINT` values must never appear in command output.

Build a binary using the [installation instructions](installation.md), or use an
existing build that includes `pm inspect`. From the component directory run:

```sh
python3 scripts/pm_conformance.py --binary /absolute/path/to/llewm
```

This standalone developer runner uses only Python's standard library and the
public CLI. It runs each input twice, compares output byte-for-byte, verifies
expected diagnostic codes, reorders set-like data, exercises invalid versions,
capabilities, conflicting mappings/revisions, project mismatches, oversized and
deep JSON, and checks its scratch directory and input for mutations. Temporary
fixture files are removed automatically. The runner launches only the executable
explicitly supplied by its caller; it is not part of the Continuity runtime.

For your own adapter, create a packet matching the schema, then run `pm inspect`
with independently known identity flags. Inspect the returned diagnostics and
authority labels before considering a later import workflow. A zero exit status
certifies this read-only interchange profile only. There is no provider-write,
retry, partial-apply or reconciliation conformance claim.
