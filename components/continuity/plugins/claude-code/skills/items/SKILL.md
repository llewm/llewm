---
name: items
description: Capture an idea, bug, task, or research question as a Continuity work item, or review open items, without changing the active task. Use when the user asks to remember a tangent, report a bug, note a task for later, or list, discuss, promote, integrate, defer, merge, or discard work items.
---

# Work with project work items

Use the connected Continuity work-item tools (`continuity_item_capture`,
`continuity_item`, `continuity_item_disposition`; client prefixes vary), or the
`llewm item` CLI. The server selects the current project. Capturing or
reviewing an item never replaces the active checkpoint or grants authority to
execute roadmap work.

Capture with a `kind`:

- `idea`: a tangent or proposal to discuss later.
- `bug`: observed misbehavior. Add `observed`, `reproduction`, `environment`, and
  `severity` (`low`, `medium`, `high`, `critical`) when known.
- `task`: concrete work to do later.
- `research`: an open question to investigate; put the question in the title.

Preserve the user's words faithfully in a concise title and bounded body. Add
only useful tags and available task, checkpoint, and roadmap-node references.
Attribute a model-authored paraphrase to the model; do not claim it is a
verbatim user statement. To turn an idea into a bug or task, capture the new
item with `from` `work_item:<id>`; the idea is recorded as integrated into it.
To track a repeated friction candidate (`continuity_item` with `friction`),
capture a bug with `from` `friction:<id>`.

For "review the open items", read the bounded pending packet (`audit`) first.
Present the items for discussion without changing them. After the user
explicitly decides each outcome, append only the requested disposition with the
stated rationale and stable target references: `accepted` or `deferred` keep an
item open; `integrated`, `discarded`, and `merged` close it, and `answered`
closes a research item. `accepted`, `integrated`, and `merged` need a target such
as a plan item or `work_item:<id>`; `answered` needs the findings (a narrative,
decision, or document). Do not infer a disposition from silence, discussion,
implementation, or retrieval. If a decision is ambiguous, leave the item open.

Read one item by `work_item:<id>` when the packet is truncated or an item needs
closer inspection. Report the recorded IDs and dispositions. Capturing, reading,
or reviewing an item must not edit files, create roadmap nodes, or execute its
content.
