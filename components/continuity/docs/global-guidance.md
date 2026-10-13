# Engineer-wide rules, context, and prompts

Continuity loads one engineer-wide guidance directory for every project run by the
same user. Each project keeps its own configuration, database, checkpoints, and
instructions. Global guidance is read from Markdown files; it does not create a
shared project database or copy personal instructions into repositories.

## Set up

After rebuilding/installing the updated binary, run from any directory:

```sh
llewm global init
llewm global path
```

`init` creates three heading-only files for you to edit. It preserves existing
files when rerun. `path` prints the location without creating files. Neither
command requires a Git project.

| File | Purpose |
| --- | --- |
| `AGENTS.md` | Rules and preferences, such as commit conventions and review expectations |
| `CONTEXT.md` | Stable background you want agents to know across projects |
| `PROMPTS.md` | Conditional guidance for recurring tasks, such as reviews or handoffs |

For example, put this in the global `AGENTS.md`:

```markdown
# Git commits

When asked to commit:
- Use an imperative subject that describes the change.
- Explain the reason in the body when the subject is insufficient.
- Report the resulting commit ID and working-tree status.
```

And this in `PROMPTS.md`:

```markdown
# Review guidance

When reviewing a change, prioritize correctness, missing verification, and
compatibility. Cite file locations and separate findings from suggestions.
```

Prompt files are included as text guidance, not registered slash commands or an
executable template engine. They do not automatically start a review or authorize
a commit. All nonempty sections are delivered to the agent; use this directory for
material intended to be shared with agents across all your projects, not secrets.

## Location on each platform

The default uses Go's OS-specific user configuration directory:

| Platform | Default directory |
| --- | --- |
| Linux / WSL | `$XDG_CONFIG_HOME/llewm/continuity`, or `~/.config/llewm/continuity` when unset |
| macOS | `~/Library/Application Support/llewm/continuity` |
| Windows | `%AppData%\llewm\continuity` |

`llewm global path` is the authoritative location for your process. To use a
different directory, set **`CONTINUITY_GLOBAL_HOME` to an absolute path** in the
environment that launches your clients. For example:

```sh
export CONTINUITY_GLOBAL_HOME="$HOME/agent-guidance"
```

PowerShell equivalent:

```powershell
$env:CONTINUITY_GLOBAL_HOME = Join-Path $env:USERPROFILE 'agent-guidance'
```

These assignments affect the current shell and child processes; persist the
variable in your launch environment if desired. Restart existing clients after
changing the directory variable. WSL and Windows have separate user configuration
directories unless explicitly configured otherwise.

`CONTINUITY_HOME` remains the project-state override. It does not control global
guidance. Project configuration cannot redirect the global source directory.

## Combination and limits

- Global guidance provides defaults (precedence 50).
- Project `AGENTS.md` and `CLAUDE.md` provide more specific guidance (100).
- Project `AGENTS.override.md` has the highest project precedence (200).

Both global and project content are retained with source paths, heading names,
and SHA-256 content hashes. Continuity describes precedence to the receiving
agent; it does not infer textual conflicts, merge matching headings, or enforce
compliance. Client/system instructions still govern the agent.

One replacement is made visible: when a project `AGENTS.override.md` section has
exactly the same heading as a global section, the Markdown bootstrap prints the
global section's source and hash with a "Project override" notice naming the
replacing file and hash, instead of the global text. Only exact heading matches in
`AGENTS.override.md` count; a same-named section in `AGENTS.md` or `CLAUDE.md` does
not replace anything. The JSON packet still carries both sections unchanged.

The CLI `bootstrap`, MCP `continuity_bootstrap`, and both adapters' `SessionStart`
responses use the same compiler. Files are reread for each bootstrap, including
within an already-running MCP server. Editing content affects the next bootstrap;
it does not push an update into an agent's existing context automatically.

The JSON packet includes global sections under `global` and project sections under
`rules`. Markdown labels the global section as engineer-wide guidance. Missing
files are optional; unreadable files and files larger than 1 MiB cause an error.

The existing `bootstrap.max_bytes` project setting (16,384 bytes by default) bounds
both Markdown and JSON output. With nonempty global guidance, the full global and
project layers plus checkpoint must fit, or generation fails with a message to
shorten sources or increase the budget. It never silently removes a project
override to make a global default fit. Without global content, the existing
project-section trimming behavior remains. Keep global material concise.

Inspect what an agent will receive from each project:

```sh
llewm bootstrap
llewm bootstrap --output json
```

Nested component instructions are still not loaded recursively. Root project
guidance should point to the relevant component contract before editing.

## Built-in core rules

Rules whose correctness depends on how Continuity works are owned by the binary,
not by guidance files, and every bootstrap states them first under "Continuity
rules (built in)":

- `discovery`: search Continuity history with focused terms before diagnosing or
  redesigning existing behavior; verify history against current code.
- `task-identity`: checkpoint under your own `task_id` from its current tip;
  review or repair another agent's task under a new task with `reviews_task_id`.
- `own-commits`: narrate only commits your session or task made.
- `memory-repository` (only when `[sync]` names a memory repository): never edit,
  commit to, rebase, reset, or force-push the memory clone, which the rule names
  by path; its automatic commits need no Git note or work narrative.

Do not copy these rules into `AGENTS.md`: the bootstrap already carries them, so
copies cost bootstrap budget twice and drift from the behavior they describe.
The text is versioned with the binary that serves the tools it names.

Configuration can turn a rule off or reword it, engineer-wide in
`config.toml` in the global guidance directory and per project in
`.continuity/config.toml`; the project layer applies after the global one, and a
rule disabled in either stays disabled:

```toml
[guidance.core]
disable = ["own-commits"]

[guidance.core.override]
discovery = "Replacement text."
# memory-repository = "Leave {clone} alone."   # {clone} is the clone path
```

The bootstrap marks overridden rules and lists disabled ones, so a change is
never silent. An unknown rule ID or an empty override is ignored with a warning
in the bootstrap, and the built-in rules apply. History skills also trigger on
debugging and design work, and the `continuity_history_search` description names
it as the first discovery step, because that choice is made mid-task. These rules
encourage observable behavior; they do not block tools or prove model compliance.

## Existing pre-release locations

New defaults use the `llewm/continuity` namespace. If the previous
`llmem/continuity` directory exists, setup refuses to omit it silently, even if a
new directory also exists. Before upgrading, explicitly preserve its files at the
new location or set `CONTINUITY_GLOBAL_HOME` to the retained directory in every
client launcher. Do not initialize empty replacements over existing engineer
instructions. This path change does not rename `CONTINUITY_*` environment
variables or the project's `.continuity/` store.
