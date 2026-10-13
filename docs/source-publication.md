# Source publication and contribution routing

Forgejo remains the authoritative Llewm source repository. The public GitHub
identity is `llewm/llewm`; its source URL is `https://github.com/llewm/llewm`.
GitHub is a one-way source mirror, separate from every project's private
Continuity History-sync repository. Public publication is pending. The reviewed
strategy is a clean initial source snapshot, a private archive of original
development history, and identical ongoing `main` history on both source remotes.

## Module and release identity

Each implemented component remains an independent Go module:

| Component | Canonical module | Command |
| --- | --- | --- |
| Continuity | `github.com/llewm/llewm/components/continuity` | `cmd/llewm` |
| Reporting | `github.com/llewm/llewm/components/reporting` | `cmd/llewm-report` |
| Action Control | `github.com/llewm/llewm/components/action-control` | `cmd/action-control` |

A module's semantic version tag includes its repository directory, such as
`components/continuity/v0.1.0-preview.1`. The version suffix passed to Go is just
`@v0.1.0-preview.1`, or a published full commit for an untagged preview. See the
[Go module directory rules](https://go.dev/ref/mod#module-directories) and
[version-pinned installation](../components/continuity/docs/installation.md#version-pinned-developer-builds).
Root `go.work` coordinates local development; each module also builds with
`GOWORK=off`. There is no root module or compatibility import path.

## Review before the first push

Complete the maintained-documentation audit before squashing development history
or creating the clean publication root. Review root and component guides against
current code: commands, paths, links, onboarding, client setup, build/update/removal,
privacy, packaging, and implemented-versus-planned behavior. Validate local examples
in isolated projects. Mark historical evidence as historical instead of presenting
it as current setup guidance.

The public snapshot keeps maintained user/developer guides, product boundaries,
and sanitized qualification findings. Archived handoffs, session trial diaries,
old review and commit inventories, and this source project's generated task plan
belong in the private development archive. Before excluding a document, retain
any useful current information in a maintained guide and repair incoming links.
Keep the original documents and their exact commit anchors recoverable through
the verified source/notes archive; pruning the snapshot must not erase that archive.

Then archive and restore-verify the original source history and Git notes privately,
sanitize the reviewed snapshot, and prepare the clean root candidate. Review the
exact candidate and remote refs before the source-history cutover and first push.
Remote version-pinned installation and native macOS/Windows qualification follow
publication; their pending status must be explicit in the documentation being
reviewed, rather than blocking this source audit.

Review the exact source commit and every ref proposed for publication. Inspect
reachable history as well as current files: removing a file from HEAD does not
remove it from history. Confirm original-source MIT licensing, dependency notices,
authorship/commit metadata, personal operational documentation, test fixtures,
and every release asset. Review any new changes since the last scan.

The October 13 preparation scan of `main` at `0922f52` covered 536 source commits
and 4,167 unique blobs (114,992,838 bytes). It found no tracked `.continuity/`
stores, database files, credential-key files, private-key headers, or matches for
the specific GitHub/OpenAI/AWS token patterns checked. This bounded pattern scan
is not a comprehensive secret audit.

The earlier scan found personal home paths in historical handoffs and the
component roadmap, and private address patterns in synthetic resource/portable
test fixtures. Those results describe the scanned historical tree, not the current
snapshot. The private source archive must retain original documents and authorship
metadata. Review and sanitize the publication snapshot before creating its new
root; never reconnect archived ancestry by merging it into the public trunk.
Original commit references in private Continuity records must remain resolvable
through the archive. The source-history cutover does not rewrite memsync history.

The initial public ref set should contain only reviewed `main` and explicitly
selected component release tags. Do not use `git push --mirror`, `--all`, or
`--follow-tags`: they can publish private work branches, notes or unrelated tags.
Do not publish `refs/notes/*`, Continuity databases, local client settings,
exported history bundles, recovery envelopes, credentials, or the managed memory
repository. Review ZIP contents, source identity and checksum manifest separately.

After publication, compare the exact remote `refs/heads/main` and each selected
tag with the approved local hashes, then qualify remote `go install` and matching
plugin installation. A configured remote or successful local build does not
establish that source or a release exists publicly.

## Ongoing one-way updates

Land reviewed changes in Forgejo first, check the exact commit and assets, then
explicitly update GitHub's `main` and selected release tags. Verify remote hashes
after each update. No automatic mirror job is configured by this preparation.
Changes proposed on GitHub are reviewed and incorporated into the authoritative
source before being mirrored back; independent edits to both remote branches
would break the one-way model.

## Contributions and reports

Use public GitHub issues and pull requests for source questions, ordinary bugs,
and proposed changes once the repository is published and those surfaces are
enabled. Include the affected component and exact version; maintainers integrate
accepted changes into Forgejo. Keep credentials, project databases, private
checkpoints, host details and unredacted transcripts out of public reports.

The [private Forgejo reporting adapter](../components/reporting/README.md) is a
separate, explicitly configured destination with a source-project allowlist. Its
automatic private submission is not permission to post to GitHub. It does not
configure a public or private security-reporting channel. Sensitive reports need
a privately agreed maintainer route rather than a public issue.

## Dependency notices

Binary ZIPs include the original-source `LICENSE` and
`components/continuity/THIRD_PARTY_NOTICES.txt`. That bundle retains upstream
license and notice text for the union of shipped-command dependencies across
the six preview targets, plus Go runtime notices. It includes nested notices
where dependencies bundle other code. Dependencies keep their original terms.

With Go and Python 3 installed, regenerate and review after dependency changes:

```sh
python3 components/continuity/scripts/dependency-notices.py
python3 components/continuity/scripts/dependency-notices.py --check
```

Run the check before packaging. Notice collection is deterministic preparation,
not a claim that arbitrary future dependencies have been reviewed.
