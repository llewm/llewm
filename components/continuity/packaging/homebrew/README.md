# Homebrew source preview

`Formula/llewm.rb` is a prepared formula for a maintainer-controlled tap. No tap,
bottle, signed installer, or Homebrew runtime qualification is claimed yet.
It builds only the Continuity executable and Bash/Zsh/Fish completions, and
installs the source license and dependency notices. Client plugins and the
optional reporting adapter are installed separately.

The formula pins the published Git commit
`43adc25cb586ac1aa4accd00dd34fa64e3dfb5bd`, with package version
`0.1.0-preview.20261013`. This package identifier is not a release tag.
Git source preserves the VCS metadata required by executable identity inspection;
the build uses `-buildvcs=true` rather than asserting a commit via linker flags.
Dependencies are downloaded in `fetch`; compilation uses `GOPROXY=off`.
Homebrew's Go dependency must satisfy the pinned component's `go.mod`.

## Qualify in a local tap

With current Homebrew already installed, run from the Llewm repository root:

```sh
brew tap-new llewm/preview
cp components/continuity/packaging/homebrew/Formula/llewm.rb \
  "$(brew --repository llewm/preview)/Formula/llewm.rb"
brew audit --strict llewm/preview/llewm
brew install --build-from-source llewm/preview/llewm
brew test llewm/preview/llewm
"$(brew --prefix llewm/preview/llewm)/bin/llewm" version --output json
```

`tap-new` creates a local tap; these commands do not publish it. Resolve an
existing installation or executable conflict before installing. Record the OS,
architecture, Homebrew/Go versions, formula source revision, and exact results.
Require the pinned clean commit in the version receipt. Homebrew's formula test
uses an isolated Git project and global/state directories.

Before reinstalling, stop clients/hooks and MCP processes. Use a disposable
project with a saved checkpoint to qualify `brew reinstall --build-from-source
llewm/preview/llewm`, then verify the checkpoint and bootstrap still recover.
When selecting a newer source commit, update the pin, package version, test
expectation, and matching client plugin together; qualify `brew upgrade` once
two formula versions exist. Same-version reinstall does not establish upgrade
behavior. Use the backup/migration gates in [installation](../../docs/installation.md)
when schemas change.

After disabling the plugin and stopping its processes, use
`brew uninstall llewm/preview/llewm`. Verify project stores, global guidance,
backups, and private memory remain. Remove the local tap with
`brew untap llewm/preview` only when no other formula uses it. Homebrew manages
its binary/completion links; preserve unrelated PATH entries and client settings.

## Qualification recorded here

Linux source-build and isolated lifecycle results are recorded in the
[installation guide](../../docs/installation.md#homebrew-source-preview).
They do not establish Homebrew dependency resolution, sandbox/link handling,
macOS execution, or live-client activation. Run the commands above on Homebrew
before publishing a tap.

The recipe follows Homebrew's [Formula Cookbook](https://docs.brew.sh/Formula-Cookbook),
[Go build helper](https://docs.brew.sh/rubydoc/Formula.html#std_go_args-instance_method),
and [completion helper](https://docs.brew.sh/rubydoc/Formula.html#generate_completions_from_executable-instance_method).
