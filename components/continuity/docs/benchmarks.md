# Benchmarks

Two harnesses run real, paid Claude Code sessions against disposable projects.
Neither runs in CI.

- `scripts/value_bench.py` asks whether Continuity helps at all. It compares
  sessions with no help, with a hand-written `NOTES.md`, and with Continuity
  records, all on the same repository, history and prompt.
- `scripts/rules_ab.py` compares two wordings of the built-in rules on
  rule-compliance fixtures. A wording passes only if compliance holds on every
  fixture and the upper bound of the cost ratio's 95% CI is below 1.

## Value benchmark

Every session gets the same codebase: `ledger`, about 30 packages with 57 tests.
Each workload starts where an earlier session or conversation stopped. The arms
differ only in what survived from that earlier work:

| Arm | What the session has |
| --- | --- |
| `none` | Git history only. No plugin, no `.llewm`. |
| `notes` | Git history plus a committed `NOTES.md` with the same facts. |
| `llewm` | Git history plus Continuity records: the task checkpoint, narratives, decisions. |
| `llewm:ARM.json` | `llewm` with a `rules_ab.py` rule override. |

Each workload's score comes from hidden tests written after the session ends,
plus the visible suite. `--self-test` checks the workloads without model calls.
In every arm, the starting state must fail the hidden tests, the reference fix
must pass, and the natural wrong fix must fail.

| Workload | Prompt | What survived from earlier work | Natural mistake without it |
| --- | --- | --- | --- |
| `tz` | Fix the off-by-one-hour report we tracked down yesterday | The root cause (EU DST start rule) and suspects already ruled out | None; it costs a fresh investigation |
| `migrate` | Continue the config migration to TOML | The user's decision to keep `auth` on INI | Migrating `auth` too |
| `cache` | Add the price caching we discussed | The rejected `lru_cache`, and the agreed 15 s TTL with invalidation | An unbounded cache or a default TTL |
| `cleanup` | Remove dead code in `ledger/services/` | A narrative and Git note: an external cron calls `legacy_export_csv` | Deleting it, since grep finds no caller |

### Results: claude-sonnet-5-5, 2026-11-11

The data comes from runs `sonnet-1` (tz, migrate, cache) and `sonnet-2` (cleanup,
plus a repeat of cache). Each cell shows passes and median cost per session.

| Workload | none | notes | llewm | llewm + discovery-v1 |
| --- | --- | --- | --- | --- |
| tz | 3/3, $0.114 | 3/3, $0.079 | 3/3, $0.074 | 3/3, $0.077 |
| migrate | 0/3 | 3/3 | 3/3 | 3/3 |
| cache (6 runs) | 0/6 | 6/6 | 6/6 | 5/5 |
| cleanup | 0/3 | 3/3 | 0/3 | 2/3 |
| **Tasks correct** | **3/15 (20%)** | **15/15** | **12/15 (80%)** | **13/14 (93%)** |

One discovery-v1 cache session did not run because of a usage limit.

What this shows:

- **Correctness.** Without Continuity, Sonnet completed only the task whose
  answer was recoverable from code. It broke the user's scope decision and
  repeated the rejected cache design every time. It also deleted code with a
  recorded external caller every time.
- **Research reuse.** On `tz`, recovered research cut the median cost by 35%
  ($0.114 → $0.074). On the other workloads, Continuity sessions cost about 10–25%
  more than `none`. They read the records and did the task the user meant,
  instead of a cheaper wrong one.
- **Notes are the honest competitor.** A hand-curated `NOTES.md` holding exactly
  the right facts matched or beat Continuity. Continuity records these facts as
  a by-product of the work, without someone writing and pruning a notes file.
  The benchmark gives the `notes` arm a perfect file.
- **Discovery is the gap.** On `cleanup`, the narrative and Git note existed,
  but the built-in discovery rule ("before diagnosing or redesigning") never
  triggered a history search before deletion: 0/3. The `discovery-v1` wording
  ("before changing, removing or redesigning existing code … code that looks
  unused, odd or naive often has a recorded reason") searched in 3/3 sessions
  and passed 2/3. The failing session searched but did not open the result.

Reproduce:

    python3 -I scripts/value_bench.py --self-test
    python3 -I scripts/value_bench.py --arms none,notes,llewm,llewm:scripts/rules_ab_arms/discovery-v1.json --runs 3

Results go to `$XDG_CACHE_HOME/llewm/value-bench/RUN/` (`results.jsonl`,
`summary.md`). A session that did not run (usage limit, auth) is recorded with
`passed: null` and excluded from the counts.

## Rules A/B

The first run compared `short-v1` (763 bytes, down from 1531) with the built-in
rules on Sonnet, 3 pairs over 5 fixtures, $2.44. Compliance was identical: 0/3 on
`discovery`, 3/3 on the rest. The cost ratio was 0.985, CI 0.929–1.055. Under the
current verdict this does not pass; the saved 610 bytes is noise against
60k–280k input tokens per session.
