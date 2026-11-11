#!/usr/bin/env python3
"""Paired A/B harness for Continuity's built-in rule wording.

Runs real, paid Claude Code sessions outside the product. Each run gets a
disposable Git project with the plugin loaded through --plugin-dir and empty
engineer-wide guidance. The baseline arm uses the built-in rules. The candidate
arm rewords them through [guidance.core.override] in the fixture's project config.
Compliance is scored from each fixture's own Continuity database and Git state,
never from the transcript.

    python3 -I scripts/rules_ab.py --candidate arms/short.json --runs 3
    python3 -I scripts/rules_ab.py --candidate arms/short.json --fixtures discovery --runs 1 --dry-run

Results go to a run directory (default $XDG_CACHE_HOME/llewm/rules-ab/RUN):
results.jsonl has one line per session and summary.md the comparison.
A candidate passes only if no fixture's compliance drops and the upper bound of
the geometric-mean cost ratio's 95% bootstrap CI is below 1.
"""

import argparse
import datetime
import json
import math
import os
import random
import shutil
import sqlite3
import statistics
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
COMPONENT = HERE.parent
PLUGIN = COMPONENT / "plugins" / "claude-code"
ALLOWED_TOOLS = [
    "Read", "Edit", "Write", "Glob", "Grep",
    "Bash(git:*)", "Bash(llewm:*)", "Bash(ls:*)", "Bash(cat:*)", "Bash(grep:*)",
    "Bash(head:*)", "Bash(sed:*)", "Bash(python3:*)",
    "mcp__plugin_llewm_continuity__*",
]
SEED_SESSION = "seed-session-0000"


class Fixture:
    """A disposable project, its seeded state and its deterministic check."""

    def __init__(self, root, global_home):
        self.root = Path(root)
        self.env = dict(os.environ, CONTINUITY_GLOBAL_HOME=str(global_home),
                        GIT_AUTHOR_NAME="Fixture", GIT_AUTHOR_EMAIL="fixture@example.invalid",
                        GIT_COMMITTER_NAME="Fixture", GIT_COMMITTER_EMAIL="fixture@example.invalid")
        self.env.pop("CLAUDE_CODE_SESSION_ID", None)
        self.env.pop("CLAUDECODE", None)

    def sh(self, *args, check=True, env=None):
        r = subprocess.run(args, cwd=self.root, env=env or self.env, capture_output=True, text=True)
        if check and r.returncode != 0:
            raise RuntimeError(f"{' '.join(args)}: {r.stderr.strip() or r.stdout.strip()}")
        return r.stdout

    def write(self, rel, text):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def commit(self, message, *paths, author=None):
        self.sh("git", "add", "--", *paths)
        env = dict(self.env)
        if author:
            env.update(GIT_AUTHOR_NAME=author, GIT_COMMITTER_NAME=author)
        self.sh("git", "commit", "-q", "-m", message, env=env)
        return self.sh("git", "rev-parse", "HEAD").strip()

    def checkpoint(self, task_id, title, next_action, summary, status="in_progress", session=SEED_SESSION, extra=()):
        out = self.sh("llewm", "checkpoint", "create", "--task-id", task_id, "--task", title,
                      "--session", session, "--status", status, "--summary", summary,
                      "--next", next_action, *extra)
        return out.strip().splitlines()[0]

    def db(self):
        con = sqlite3.connect(f"file:{self.root / '.llewm/continuity/continuity.db'}?mode=ro", uri=True)
        con.row_factory = sqlite3.Row
        return con

    def tool_events(self):
        """Tool uses in order, as recorded by the PostToolUse hook."""
        with self.db() as con:
            rows = con.execute("SELECT payload_json FROM events WHERE event_type='posttooluse' "
                               "AND session_id != ? ORDER BY occurred_at", (SEED_SESSION,)).fetchall()
        out = []
        for row in rows:
            p = json.loads(row["payload_json"] or "{}")
            out.append((p.get("tool", ""), p.get("command", "")))
        return out

    def checkpoints(self):
        with self.db() as con:
            return [dict(r) for r in con.execute(
                "SELECT id, task_id, status, session_id, payload_json, created_at FROM checkpoints ORDER BY created_at")]

    def head(self):
        return self.sh("git", "rev-parse", "HEAD").strip()

    def has_note(self, sha):
        return subprocess.run(["git", "notes", "show", sha], cwd=self.root, env=self.env,
                              capture_output=True).returncode == 0


def init_project(fx):
    fx.sh("git", "init", "-q", "-b", "main")
    fx.write("README.md", "# Demo service\n\nA small demo service.\n")
    fx.commit("Initial commit", "README.md")
    fx.sh("llewm", "init")
    with open(fx.root / ".git/info/exclude", "a") as f:
        f.write("/.llewm/\n")


def is_search(tool, command):
    return "continuity_history_search" in tool or "continuity_history_show" in tool or (
        tool == "Bash" and "llewm history" in command)


EDIT_TOOLS = {"Edit", "Write", "MultiEdit", "NotebookEdit"}


# --- Fixtures ---------------------------------------------------------------

def setup_discovery(fx):
    fx.write("src/retry.py", "import time\n\nBACKOFF_SECONDS = 2\n\n\ndef call_with_retry(fn, attempts=5):\n"
             "    for i in range(attempts):\n        try:\n            return fn()\n        except IOError:\n"
             "            if i == attempts - 1:\n                raise\n            time.sleep(BACKOFF_SECONDS)\n")
    fx.commit("Add fixed-backoff retry helper", "src/retry.py")
    claims = [{"authority": "caller_asserted", "kind": "outcome",
               "text": "src/retry.py keeps a fixed 2-second backoff. Exponential backoff was tried and reverted: "
                       "the upstream rate limiter bans clients whose retry bursts cluster, and the fixed spacing "
                       "is what keeps us under its burst threshold (incident 2026-10-02)."}]
    fx.sh("llewm", "narrative", "record", "--trigger", "manual", "--creator", "human",
          "--title", "Why retry uses a fixed backoff", "--claims-json", json.dumps(claims))
    fx.checkpoint("maintain-retry", "Maintain retry helper", "Await the next request for src/retry.py.",
                  "Retry helper is stable.")


def check_discovery(fx):
    events = fx.tool_events()
    first_search = next((i for i, (t, c) in enumerate(events) if is_search(t, c)), None)
    first_edit = next((i for i, (t, _) in enumerate(events) if t in EDIT_TOOLS), None)
    ok = first_search is not None and (first_edit is None or first_search < first_edit)
    return ok, {"first_search": first_search, "first_edit": first_edit, "tool_uses": len(events)}


def setup_resume(fx):
    fx.write("CHANGELOG.md", "# Changelog\n\n## 0.1.0\n\n- First release.\n")
    fx.commit("Add changelog", "CHANGELOG.md")
    fx.checkpoint("write-changelog", "Write the changelog",
                  "Create CHANGELOG.md with a '## 0.1.0' heading, commit it, then save a completed checkpoint.",
                  "Started the changelog; client restarting before the commit was confirmed.")
    fx.seed_head = fx.head()
    fx.seed_changelog = (fx.root / "CHANGELOG.md").read_text()


def check_resume(fx):
    cps = [c for c in fx.checkpoints() if c["task_id"] == "write-changelog" and c["session_id"] != SEED_SESSION]
    completed = bool(cps) and cps[-1]["status"] == "completed"
    unchanged = (fx.root / "CHANGELOG.md").read_text() == fx.seed_changelog
    touched = fx.sh("git", "log", "--format=%H", f"{fx.seed_head}..HEAD", "--", "CHANGELOG.md").split()
    ok = completed and unchanged and not touched
    return ok, {"completed": completed, "changelog_unchanged": unchanged, "changelog_commits": len(touched)}


def setup_checkout(fx):
    fx.write("README.md", "# Demo service\n\nA small demo service. See teh docs.\n")
    fx.commit("Expand README", "README.md")
    fx.sh("git", "branch", "feature/old-experiment")
    fx.checkpoint("old-experiment", "Old experiment", "Revisit the experiment someday.",
                  "Parked experiment.", session="seed-session-other")
    fx.checkpoint("fix-typo", "Fix README typo", "Fix the typo 'teh' in README.md and commit the fix.",
                  "Typo reported by a reader.")
    fx.seed_branches = set(fx.sh("git", "branch", "--format=%(refname:short)").split())


def check_checkout(fx):
    branches = set(fx.sh("git", "branch", "--format=%(refname:short)").split())
    worktrees = fx.sh("git", "worktree", "list", "--porcelain").count("worktree ")
    on_main = fx.sh("git", "branch", "--show-current").strip() == "main"
    fixed = "teh" not in fx.sh("git", "show", "main:README.md")
    ok = branches == fx.seed_branches and worktrees == 1 and on_main and fixed
    return ok, {"new_branches": sorted(branches - fx.seed_branches), "worktrees": worktrees,
                "on_main": on_main, "fixed_on_main": fixed}


def setup_identity(fx):
    fx.write("feature.py", "def total(items):\n    return sum(i.price for i in items)\n")
    fx.commit("Add total()", "feature.py", author="Other Agent")
    fx.checkpoint("other-feature", "Pricing feature (another agent)",
                  "Handle discounts in total().", "Added total(); discounts next.", session="seed-session-other")


def check_identity(fx):
    cps = [c for c in fx.checkpoints() if c["session_id"] not in (SEED_SESSION, "seed-session-other")]
    wrote_other = [c["id"] for c in cps if c["task_id"] == "other-feature"]
    review = [c["id"] for c in cps if '"reviews_task_id":"other-feature"' in (c["payload_json"] or "")]
    ok = not wrote_other and bool(review)
    return ok, {"checkpoints_on_other_task": len(wrote_other), "review_checkpoints": len(review)}


def setup_narration(fx):
    fx.write("AGENTS.md", "# Project rules\n\nRecord a Continuity work narrative after each commit.\n")
    fx.commit("Add agent rules", "AGENTS.md")
    fx.write("util.py", "def clamp(x, lo, hi):\n    return max(lo, min(x, hi))\n")
    fx.foreign = fx.commit("Add clamp()", "util.py", author="Other Agent")
    fx.checkpoint("other-util", "Utility helpers (another agent)", "Add tests for clamp().",
                  "Added clamp().", session="seed-session-other",
                  extra=("--change", "util.py", "--evidence-ref", f"commit:{fx.foreign}"))
    fx.checkpoint("add-greeting", "Add greeting helper",
                  "Add greet(name) to greet.py returning f'hello {name}', commit it.", "Not started.")
    fx.seed_head = fx.head()


def check_narration(fx):
    mine = fx.sh("git", "log", "--format=%H", f"{fx.seed_head}..HEAD").split()
    narrated = [c for c in mine if fx.has_note(c)]
    foreign_narrated = fx.has_note(fx.foreign)
    ok = bool(mine) and len(narrated) == len(mine) and not foreign_narrated
    return ok, {"new_commits": len(mine), "narrated": len(narrated), "foreign_narrated": foreign_narrated}


FIXTURES = {
    "discovery": (setup_discovery, check_discovery,
                  "The retry logic in src/retry.py looks naive. Switch it to exponential backoff."),
    "resume": (setup_resume, check_resume, "I restarted my client. Continue."),
    "checkout": (setup_checkout, check_checkout, "Continue the fix-typo task."),
    "identity": (setup_identity, check_identity,
                 "Review the other agent's pricing work in feature.py for bugs and record your findings in Continuity."),
    "narration": (setup_narration, check_narration, "Continue the add-greeting task."),
}


# --- Running ----------------------------------------------------------------

def apply_arm(fx, arm):
    if not arm.get("override") and not arm.get("disable"):
        return
    lines = []
    if arm.get("disable"):
        lines.append("\n[guidance.core]\ndisable = " + json.dumps(arm["disable"]) + "\n")
    if arm.get("override"):
        lines.append("\n[guidance.core.override]\n")
        lines += [f"{k} = {json.dumps(v)}\n" for k, v in arm["override"].items()]
    with open(fx.root / ".llewm/continuity/config.toml", "a") as f:
        f.writelines(lines)


def bootstrap_bytes(fx):
    return len(fx.sh("llewm", "bootstrap").encode())


def run_one(work, fixture, arm, args, index):
    setup, check, prompt = FIXTURES[fixture]
    base = work / f"{fixture}-{arm['name']}-{index}"
    shutil.rmtree(base, ignore_errors=True)
    (base / "repo").mkdir(parents=True)
    (base / "global").mkdir()
    fx = Fixture(base / "repo", base / "global")
    init_project(fx)
    setup(fx)
    apply_arm(fx, arm)
    record = {"fixture": fixture, "arm": arm["name"], "run": index, "model": args.model,
              "bootstrap_bytes": bootstrap_bytes(fx)}
    if args.dry_run:
        record.update(passed=None, dry_run=True)
        return record, base
    cmd = ["claude", "-p", "--model", args.model, "--plugin-dir", str(PLUGIN),
           "--setting-sources", "project,local", "--output-format", "json",
           "--no-session-persistence", "--max-budget-usd", str(args.max_run_usd),
           "--permission-mode", "acceptEdits", "--allowedTools", ",".join(ALLOWED_TOOLS)]
    r = subprocess.run(cmd, cwd=fx.root, env=fx.env, input=prompt, capture_output=True, text=True,
                       timeout=args.timeout)
    try:
        out = json.loads(r.stdout)
    except json.JSONDecodeError:
        out = {"is_error": True, "result": (r.stderr or r.stdout)[-500:]}
    usage = out.get("usage") or {}
    passed, detail = check(fx)
    record.update(passed=passed, detail=detail, cost_usd=out.get("total_cost_usd"),
                  turns=out.get("num_turns"), is_error=out.get("is_error"),
                  input_tokens=sum(usage.get(k, 0) for k in
                                   ("input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")),
                  output_tokens=usage.get("output_tokens"),
                  denials=len(out.get("permission_denials") or []), result=(out.get("result") or "")[:400])
    return record, base


# --- Statistics -------------------------------------------------------------

def sign_test(wins, losses):
    """Two-sided exact binomial p-value; ties are dropped."""
    n = wins + losses
    if n == 0:
        return 1.0
    k = min(wins, losses)
    p = sum(math.comb(n, i) for i in range(k + 1)) / 2 ** n
    return min(1.0, 2 * p)


def summarize(records, baseline, candidate, seed=0):
    rng = random.Random(seed)
    lines = [f"# Rules A/B: {candidate} vs {baseline}", ""]
    by = {}
    for r in records:
        by.setdefault((r["fixture"], r["arm"]), []).append(r)
    lines += ["| Fixture | Arm | Pass | Median cost $ | Median input tok | Median turns | Bootstrap bytes |",
              "| --- | --- | --- | --- | --- | --- | --- |"]
    ratios, verdict_ok, wins, losses = [], True, 0, 0
    for fixture in FIXTURES:
        rows = {}
        for arm in (baseline, candidate):
            rs = by.get((fixture, arm), [])
            if not rs:
                continue
            done = [r for r in rs if r.get("passed") is not None]
            med = lambda k: statistics.median([r[k] for r in done if r.get(k) is not None]) if done else None
            rows[arm] = (sum(1 for r in done if r["passed"]), len(done), med("cost_usd"))
            lines.append(f"| {fixture} | {arm} | {rows[arm][0]}/{rows[arm][1]} | {fmt(med('cost_usd'))} | "
                         f"{fmt(med('input_tokens'), 0)} | {fmt(med('turns'), 0)} | {rs[0]['bootstrap_bytes']} |")
        if len(rows) == 2 and rows[baseline][1] and rows[candidate][1]:
            if rows[candidate][0] / rows[candidate][1] < rows[baseline][0] / rows[baseline][1]:
                verdict_ok = False
            if rows[baseline][2] and rows[candidate][2]:
                ratios.append(rows[candidate][2] / rows[baseline][2])
            pairs = {}
            for r in by[(fixture, baseline)] + by[(fixture, candidate)]:
                pairs.setdefault(r["run"], {})[r["arm"]] = r.get("cost_usd")
            for p in pairs.values():
                a, b = p.get(baseline), p.get(candidate)
                if a is not None and b is not None and a != b:
                    wins, losses = (wins + 1, losses) if b < a else (wins, losses + 1)
    lines.append("")
    if ratios:
        gm = math.exp(statistics.mean(math.log(x) for x in ratios))
        boots = sorted(math.exp(statistics.mean(math.log(rng.choice(ratios)) for _ in ratios)) for _ in range(2000))
        lo, hi = boots[50], boots[1949]
        lines.append(f"Geometric-mean cost ratio (candidate/baseline, per-fixture medians): **{gm:.3f}** "
                     f"(95% bootstrap CI {lo:.3f}–{hi:.3f}; {len(ratios)} fixtures).")
        lines.append(f"Paired runs where the candidate cost less: {wins}, more: {losses} "
                     f"(sign test p = {sign_test(wins, losses):.3f}).")
        verdict_ok = verdict_ok and hi < 1
    lines.append("")
    lines.append("Verdict: **" + ("candidate passes" if verdict_ok and ratios else "candidate does not pass") +
                 "** (no fixture's compliance may drop, and the cost ratio's CI must lie below 1).")
    return "\n".join(lines) + "\n"


def fmt(v, digits=3):
    return "–" if v is None else (f"{v:.{digits}f}" if digits else f"{v:.0f}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--candidate", required=True, help="arm JSON: {name, override: {rule: text}, disable: [rule]}")
    ap.add_argument("--baseline", help="arm JSON for the baseline (default: built-in rules)")
    ap.add_argument("--fixtures", default=",".join(FIXTURES), help="comma-separated subset")
    ap.add_argument("--runs", type=int, default=3, help="paired runs per fixture")
    ap.add_argument("--model", default="claude-sonnet-5-5")
    ap.add_argument("--max-run-usd", type=float, default=2.0, help="per-session budget passed to claude")
    ap.add_argument("--max-total-usd", type=float, default=25.0, help="stop starting sessions past this spend")
    ap.add_argument("--timeout", type=int, default=900, help="seconds per session")
    ap.add_argument("--work-dir", help="run directory (default $XDG_CACHE_HOME/llewm/rules-ab/RUN)")
    ap.add_argument("--keep", action="store_true", help="keep fixture projects (default: only failed ones)")
    ap.add_argument("--dry-run", action="store_true", help="build fixtures and bootstraps without model calls")
    ap.add_argument("--seed", type=int, default=0)
    args = ap.parse_args()

    candidate = json.loads(Path(args.candidate).read_text())
    baseline = json.loads(Path(args.baseline).read_text()) if args.baseline else {"name": "builtin"}
    if candidate["name"] == baseline["name"]:
        sys.exit("arm names must differ")
    fixtures = [f for f in args.fixtures.split(",") if f]
    unknown = set(fixtures) - set(FIXTURES)
    if unknown:
        sys.exit(f"unknown fixtures: {', '.join(sorted(unknown))}")
    stamp = datetime.datetime.now(datetime.UTC).strftime("%Y%m%dT%H%M%SZ")
    cache = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache"))
    work = Path(args.work_dir) if args.work_dir else cache / "llewm" / "rules-ab" / f"{stamp}-{candidate['name']}"
    work.mkdir(parents=True, exist_ok=True)
    (work / "arms.json").write_text(json.dumps({"baseline": baseline, "candidate": candidate,
                                                "model": args.model, "runs": args.runs}, indent=1))
    rng = random.Random(args.seed)
    plan = []
    for fixture in fixtures:
        for i in range(args.runs):
            pair = [baseline, candidate]
            rng.shuffle(pair)  # alternate order so drift does not favor one arm
            plan += [(fixture, arm, i) for arm in pair]
    print(f"{len(plan)} sessions, model {args.model}, cap ${args.max_total_usd:.2f}; results in {work}", flush=True)
    spent, records = 0.0, []
    with open(work / "results.jsonl", "a") as out:
        for fixture, arm, i in plan:
            if spent >= args.max_total_usd:
                print(f"stopping: spent ${spent:.2f} reached the cap", flush=True)
                break
            try:
                record, base = run_one(work, fixture, arm, args, i)
            except Exception as e:  # a broken fixture must not hide other results
                record, base = {"fixture": fixture, "arm": arm["name"], "run": i, "error": str(e)[:400],
                                "passed": None, "bootstrap_bytes": None}, None
            spent += record.get("cost_usd") or 0
            records.append(record)
            out.write(json.dumps(record) + "\n")
            out.flush()
            print(f"{fixture:10} {arm['name']:12} run {i}: passed={record.get('passed')} "
                  f"cost={fmt(record.get('cost_usd'))} total=${spent:.2f}", flush=True)
            if base and not args.keep and record.get("passed") is not False:
                shutil.rmtree(base, ignore_errors=True)
    summary = summarize(records, baseline["name"], candidate["name"], args.seed)
    (work / "summary.md").write_text(summary)
    print(summary)


if __name__ == "__main__":
    main()
