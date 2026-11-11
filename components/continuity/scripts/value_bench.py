#!/usr/bin/env python3
"""With-versus-without benchmark for Llewm Continuity on multi-session work.

Runs real, paid Claude Code sessions outside the product. Every session gets a fresh
copy of one small but realistic codebase ("ledger"). Each workload starts where
an earlier session or conversation stopped. All arms get the same repository,
the same commit history and the same prompt. They differ only in what survived
that earlier session:

  none    Git history only. No plugin and no .llewm directory.
  notes   Git history plus a committed NOTES.md holding the same facts by hand.
  llewm   Git history plus Continuity records: checkpoints, narratives, decisions.
          llewm:ARM.json applies a rules_ab.py arm (rule overrides) as well.

Each workload is scored by hidden tests written after the session ends, plus the
visible test suite. Pass/fail never comes from the transcript.

    python3 -I scripts/value_bench.py --self-test
    python3 -I scripts/value_bench.py --arms none,llewm --runs 3
    python3 -I scripts/value_bench.py --arms none,llewm:scripts/rules_ab_arms/x.json --workloads cleanup

Results go to a run directory (default $XDG_CACHE_HOME/llewm/value-bench/RUN):
results.jsonl has one line per session and summary.md the comparison.
"""

import argparse
import datetime
import json
import math
import os
import random
import shutil
import statistics
import subprocess
import sys
import textwrap
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from rules_ab import ALLOWED_TOOLS, PLUGIN, Fixture, apply_arm  # noqa: E402

SEED_SESSION = "seed-session-0000"
BENCH_TOOLS = ALLOWED_TOOLS + ["Bash(pytest:*)", "Bash(python:*)", "Bash(find:*)", "Bash(wc:*)"]


def dedent(s):
    return textwrap.dedent(s).lstrip("\n")


# --- The shared codebase ---------------------------------------------------

FILLER = ["accounts", "invoices", "payments", "refunds", "taxes", "currency", "audit", "users",
          "permissions", "webhooks", "metrics", "features", "discounts", "subscriptions", "customers",
          "vendors", "shipping", "inventory", "returns", "loyalty", "contracts", "budgets", "forecasts",
          "importers"]

FILLER_MODULE = '''"""{title} records: parsing, validation and totals."""

from dataclasses import dataclass, field


@dataclass
class {cls}:
    id: str
    status: str = "active"
    amount_cents: int = 0
    tags: list = field(default_factory=list)


def normalize_{name}(record):
    """Return a copy of record with stripped strings and a lower-case status."""
    out = {{k: v.strip() if isinstance(v, str) else v for k, v in record.items()}}
    if "status" in out:
        out["status"] = out["status"].lower()
    return out


def validate_{name}(record, required=("id", "status")):
    """Return the required fields record lacks."""
    return [k for k in required if not record.get(k)]


def total_{name}_cents(records):
    """Sum amount_cents over active records."""
    return sum(r.get("amount_cents", 0) for r in records if r.get("status") == "active")


def group_{name}_by_status(records):
    groups = {{}}
    for r in records:
        groups.setdefault(r.get("status", "unknown"), []).append(r)
    return groups


def parse_{name}(line):
    """Parse 'id,status,amount_cents' into a {cls}."""
    ident, status, amount = (part.strip() for part in line.split(","))
    return {cls}(ident, status.lower(), int(amount))
'''

FILLER_TEST = '''from ledger.{name}.core import parse_{name}, total_{name}_cents, validate_{name}


def test_parse_and_total():
    rec = parse_{name}("x1, Active, 250")
    assert rec.status == "active" and rec.amount_cents == 250
    assert total_{name}_cents([{{"status": "active", "amount_cents": 3}}, {{"status": "closed", "amount_cents": 5}}]) == 3


def test_validate():
    assert validate_{name}({{"id": "a"}}) == ["status"]
'''

CLOCK_BUGGY = dedent('''
    """Daylight-saving rules for the regions we bill in.

    A rule edge is (month, nth, weekday, hour). nth >= 1 counts from the start of
    the month; nth == -1 is the last such weekday. Times are local wall-clock.
    """

    import datetime as dt

    SUN = 6

    RULES = {
        "US": {"start": (3, 2, SUN, 2), "end": (11, 1, SUN, 2)},
        "EU": {"start": (3, 1, SUN, 1), "end": (10, -1, SUN, 1)},
    }

    ZONES = {
        "America/New_York": ("US", -5),
        "America/Chicago": ("US", -6),
        "Europe/Berlin": ("EU", 1),
        "Europe/London": ("EU", 0),
    }


    def nth_weekday(year, month, nth, weekday):
        if nth > 0:
            d = dt.date(year, month, 1)
            d += dt.timedelta(days=(weekday - d.weekday()) % 7)
            return d + dt.timedelta(weeks=nth - 1)
        d = dt.date(year + month // 12, month % 12 + 1, 1) - dt.timedelta(days=1)
        return d - dt.timedelta(days=(d.weekday() - weekday) % 7)


    def transition(year, region, edge):
        month, nth, weekday, hour = RULES[region][edge]
        return dt.datetime.combine(nth_weekday(year, month, nth, weekday), dt.time(hour))
''')

CLOCK_INIT = dedent('''
    """Wall-clock to UTC conversion for account time zones."""

    import datetime as dt

    from .rules import ZONES, transition


    def utc_offset(zone, local):
        """Hours east of UTC for a naive local datetime in zone."""
        region, standard = ZONES[zone]
        start = transition(local.year, region, "start")
        end = transition(local.year, region, "end")
        return standard + (1 if start <= local < end else 0)


    def to_utc(zone, local):
        return local - dt.timedelta(hours=utc_offset(zone, local))
''')

REPORTS_SINCE = dedent('''
    """Entries recorded at or after a UTC instant (`ledger report --since`)."""

    from ledger.clock import to_utc


    def entries_since(entries, since_utc):
        """entries are dicts with an account 'zone' and a naive local 'at'."""
        out = []
        for e in entries:
            at = to_utc(e["zone"], e["at"])
            if at >= since_utc:
                out.append(dict(e, at_utc=at))
        return sorted(out, key=lambda e: e["at_utc"])
''')

REPORTS_FORMAT = dedent('''
    """Render report rows."""


    def format_row(entry):
        stamp = entry["at_utc"].strftime("%Y-%m-%d %H:%M")
        return f"{stamp}Z  {entry['account']:<12} {entry['amount_cents'] / 100:>10.2f}"


    def format_report(entries):
        return "\\n".join(format_row(e) for e in entries)
''')

CLI = dedent('''
    """Command line entry point: python -m ledger.cli report --since 2026-03-01T00:00"""

    import argparse
    import datetime as dt
    import json
    import sys

    from ledger.reports.format import format_report
    from ledger.reports.since import entries_since


    def main(argv=None):
        ap = argparse.ArgumentParser(prog="ledger")
        sub = ap.add_subparsers(dest="cmd", required=True)
        rep = sub.add_parser("report")
        rep.add_argument("--since", required=True, help="UTC instant, ISO format")
        rep.add_argument("--entries", default="-", help="JSON lines file, - for stdin")
        args = ap.parse_args(argv)
        src = sys.stdin if args.entries == "-" else open(args.entries)
        entries = []
        for line in src:
            e = json.loads(line)
            e["at"] = dt.datetime.fromisoformat(e["at"])
            entries.append(e)
        print(format_report(entries_since(entries, dt.datetime.fromisoformat(args.since))))


    if __name__ == "__main__":
        main()
''')

CONFIG_LOAD = dedent('''
    """Shared config readers."""

    import configparser
    import tomllib
    import warnings


    def read_toml(path):
        """Return the parsed TOML file, or None when it does not exist."""
        if not path.exists():
            return None
        with open(path, "rb") as f:
            return tomllib.load(f)


    def read_ini(path, section):
        cp = configparser.ConfigParser()
        if not cp.read(path):
            raise FileNotFoundError(path)
        return dict(cp[section])


    def read_ini_fallback(path, section):
        """Read a legacy INI file, warning that it is deprecated."""
        warnings.warn(f"{path.name}: INI config is deprecated; convert it to TOML",
                      DeprecationWarning, stacklevel=2)
        return read_ini(path, section)
''')

CONFIG_TOML = '''"""{title} settings."""

from ._load import read_ini_fallback, read_toml


def load(root):
    data = read_toml(root / "config" / "{name}.toml")
    if data is None:
        data = read_ini_fallback(root / "config" / "{name}.ini", "{name}")
    return {body}
'''

CONFIG_INI = '''"""{title} settings."""

from ._load import read_ini


def load(root):
    data = read_ini(root / "config" / "{name}.ini", "{name}")
    return {body}
'''

CONFIGS = {  # name: (title, {key: (value, kind)})
    "db": ("Database", {"host": ("db.internal", "str"), "port": (5432, "int")}),
    "cache": ("Cache", {"url": ("redis://cache.internal:6379/0", "str"), "ttl_seconds": (300, "int")}),
    "mail": ("Mail", {"smtp_host": ("smtp.internal", "str"), "smtp_port": (587, "int"),
                      "sender": ("billing@example.invalid", "str")}),
    "queue": ("Queue", {"broker": ("amqp://mq.internal", "str"), "prefetch": (8, "int")}),
    "auth": ("Auth", {"issuer": ("https://id.example.invalid", "str"), "client_id": ("ledger", "str"),
                      "client_secret_file": ("/run/secrets/ledger-auth", "str")}),
}


def config_body(keys):
    parts = [f'"{k}": int(data["{k}"])' if kind == "int" else f'"{k}": data["{k}"]'
             for k, (_, kind) in keys.items()]
    return "{" + ", ".join(parts) + "}"


def config_toml_text(keys):
    return "".join(f'{k} = {json.dumps(v)}\n' for k, (v, _) in keys.items())


def config_ini_text(name, keys):
    return f"[{name}]\n" + "".join(f"{k} = {v}\n" for k, (v, _) in keys.items())


def config_module(name, toml):
    title, keys = CONFIGS[name]
    return (CONFIG_TOML if toml else CONFIG_INI).format(title=title, name=name, body=config_body(keys))


CONFIG_TEST = dedent('''
    from pathlib import Path

    import pytest

    from ledger.config import auth, cache, db, mail, queue

    ROOT = Path(__file__).resolve().parents[1]


    @pytest.mark.parametrize("mod", [db, cache, mail, queue, auth])
    def test_repo_config_loads(mod):
        assert mod.load(ROOT)
''')

STORAGE_PRICES = dedent('''
    """Price storage. The admin tool also writes here directly."""

    _PRICES = {}
    CALLS = 0


    def fetch_price(sku):
        """Return the price of sku in cents. Each call is a database round trip."""
        global CALLS
        CALLS += 1
        return _PRICES[sku]


    def store_price(sku, cents):
        _PRICES[sku] = cents
''')

CATALOG = dedent('''
    """Price lookups for checkout and invoicing."""

    from ledger.storage import prices


    def get_price(sku):
        """Current price of sku in cents."""
        return prices.fetch_price(sku)


    def update_price(sku, cents):
        prices.store_price(sku, cents)
''')

PRICING_TEST = dedent('''
    from ledger.pricing import catalog
    from ledger.storage import prices


    def test_get_and_update():
        prices.store_price("t-1", 100)
        assert catalog.get_price("t-1") == 100
        catalog.update_price("t-1", 120)
        assert catalog.get_price("t-1") == 120
''')

MAINTENANCE = dedent('''
    """Maintenance jobs."""

    import csv
    import io

    from ledger.search.index import build_index


    def purge_stale_sessions(sessions, now, max_age):
        """Drop sessions not seen within max_age seconds."""
        return [s for s in sessions if now - s["seen"] <= max_age]


    def rebuild_search_index(records):
        return build_index(records)


    def legacy_export_csv(rows):
        """Write rows in the v1 CSV layout."""
        buf = io.StringIO()
        w = csv.writer(buf)
        w.writerow(["id", "amount", "currency"])
        for r in rows:
            w.writerow([r["id"], f"{r['amount_cents'] / 100:.2f}", r.get("currency", "USD")])
        return buf.getvalue()


    def migrate_v1_accounts(accounts):
        """Convert v1 account dicts (name, balance) to v2 (display_name, balance_cents)."""
        return [{"display_name": a["name"], "balance_cents": int(a["balance"] * 100)} for a in accounts]


    def compute_legacy_fee(amount_cents):
        """Flat 2.5% fee from the 2023 pricing."""
        return round(amount_cents * 0.025)


    def _old_rate_table():
        return {"standard": 250, "express": 900}
''')

NOTIFY = dedent('''
    """Outbound notifications."""


    def send_email(outbox, to, subject, body):
        outbox.append({"to": to, "subject": subject, "body": body})
        return len(outbox)


    def send_fax(number, body):
        raise NotImplementedError("fax gateway retired")
''')

JOBS = dedent('''
    """Scheduled jobs."""

    from ledger.services.maintenance import purge_stale_sessions, rebuild_search_index
    from ledger.services.notify import send_email


    def nightly(state, now):
        state["sessions"] = purge_stale_sessions(state["sessions"], now, 86400)
        state["index"] = rebuild_search_index(state["records"])
        send_email(state["outbox"], "ops@example.invalid", "nightly done", f"{len(state['records'])} records")
        return state
''')

SEARCH = dedent('''
    """Inverted index over ledger memo text."""

    import re

    STOP = {"the", "a", "an", "of", "to", "and"}


    def tokenize(text):
        return [t for t in re.findall(r"[a-z0-9]+", text.lower()) if t not in STOP]


    def build_index(records):
        index = {}
        for rid, text in records:
            for pos, term in enumerate(tokenize(text)):
                index.setdefault(term, []).append((rid, pos))
        return index
''')

SERVICES_TEST = dedent('''
    from ledger.services.jobs import nightly


    def test_nightly():
        state = {"sessions": [{"seen": 0}, {"seen": 90000}], "records": [(1, "paid the invoice")], "outbox": []}
        out = nightly(state, 100000)
        assert len(out["sessions"]) == 1 and "invoice" in out["index"] and len(out["outbox"]) == 1
''')

CLOCK_TEST = dedent('''
    import datetime as dt

    from ledger.clock import to_utc


    def test_new_york_summer_and_winter():
        assert to_utc("America/New_York", dt.datetime(2026, 7, 1, 12)) == dt.datetime(2026, 7, 1, 16)
        assert to_utc("America/New_York", dt.datetime(2026, 1, 15, 12)) == dt.datetime(2026, 1, 15, 17)


    def test_berlin_july():
        assert to_utc("Europe/Berlin", dt.datetime(2026, 7, 1, 12)) == dt.datetime(2026, 7, 1, 10)
''')


def build_codebase(fx):
    """Write the ledger project as a plausible series of commits."""
    fx.sh("git", "init", "-q", "-b", "main")
    fx.write("README.md", dedent("""
        # ledger

        Billing ledger service: accounts, invoices, pricing and reports.

        Run the tests with `pytest -q`.
        """))
    fx.write("pyproject.toml", '[project]\nname = "ledger"\nversion = "1.4.0"\nrequires-python = ">=3.11"\n\n'
             '[tool.pytest.ini_options]\ntestpaths = ["tests"]\n')
    fx.write("ledger/__init__.py", '"""Billing ledger."""\n')
    fx.write("tests/conftest.py", "import sys\nfrom pathlib import Path\n\n"
             "sys.path.insert(0, str(Path(__file__).resolve().parents[1]))\n")
    fx.write(".gitignore", "__pycache__/\n.pytest_cache/\n")
    fx.commit("Initial ledger skeleton", ".")
    for i, name in enumerate(FILLER):
        fx.write(f"ledger/{name}/__init__.py", "")
        fx.write(f"ledger/{name}/core.py", FILLER_MODULE.format(
            name=name, title=name.capitalize(), cls=name.capitalize().rstrip("s") + "Record"))
        fx.write(f"tests/test_{name}.py", FILLER_TEST.format(name=name))
        if i % 6 == 5:
            fx.commit(f"Add {', '.join(FILLER[i - 5:i + 1])} records", ".")
    fx.write("ledger/clock/__init__.py", CLOCK_INIT)
    fx.write("ledger/clock/rules.py", CLOCK_BUGGY)
    fx.write("tests/test_clock.py", CLOCK_TEST)
    fx.commit("Add clock rules for US and EU daylight saving", ".")
    fx.write("ledger/reports/__init__.py", "")
    fx.write("ledger/reports/since.py", REPORTS_SINCE)
    fx.write("ledger/reports/format.py", REPORTS_FORMAT)
    fx.write("ledger/cli.py", CLI)
    fx.commit("Add `ledger report --since`", ".")
    fx.write("ledger/search/__init__.py", "")
    fx.write("ledger/search/index.py", SEARCH)
    fx.write("ledger/services/__init__.py", "")
    fx.write("ledger/services/maintenance.py", MAINTENANCE)
    fx.write("ledger/services/notify.py", NOTIFY)
    fx.write("ledger/services/jobs.py", JOBS)
    fx.write("tests/test_services.py", SERVICES_TEST)
    fx.commit("Add search index and nightly maintenance jobs", ".")
    fx.write("ledger/storage/__init__.py", "")
    fx.write("ledger/storage/prices.py", STORAGE_PRICES)
    fx.write("ledger/pricing/__init__.py", "")
    fx.write("ledger/pricing/catalog.py", CATALOG)
    fx.write("tests/test_pricing.py", PRICING_TEST)
    fx.commit("Add price catalog over storage", ".")
    fx.write("ledger/config/__init__.py", "")
    fx.write("ledger/config/_load.py", CONFIG_LOAD)
    for name, (_, keys) in CONFIGS.items():
        fx.write(f"ledger/config/{name}.py", config_module(name, toml=False))
        fx.write(f"config/{name}.ini", config_ini_text(name, keys))
    fx.write("tests/test_config.py", CONFIG_TEST)
    fx.commit("Add INI config loaders", ".")
    for name in ("db", "cache"):
        fx.write(f"ledger/config/{name}.py", config_module(name, toml=True))
        fx.write(f"config/{name}.toml", config_toml_text(CONFIGS[name][1]))
        fx.sh("git", "rm", "-q", f"config/{name}.ini")
        fx.commit(f"Migrate {name} config to TOML", ".")
    fx.legacy_export_commit = fx.head()  # the commit that holds legacy_export_csv
    fx.seed_head = fx.head()


# --- Workloads --------------------------------------------------------------
#
# Each workload has: a prompt (same for every arm); the Continuity records an
# earlier session would have left (task checkpoint, narratives, decisions); the
# same facts as NOTES.md text; hidden tests; and, for --self-test, a reference
# solution and the natural wrong solution the hidden tests must reject.

HIDDEN_HEAD = "import datetime as dt\nimport os\nimport pathlib\n\nREPO = pathlib.Path(os.environ['BENCH_REPO'])\n"

WORKLOADS = {}


def workload(name):
    def register(cls):
        WORKLOADS[name] = cls
        return cls
    return register


@workload("tz")
class TZ:
    """Research reuse: the root cause was found last session, the fix was not written."""
    prompt = ("Yesterday we tracked down why `ledger report --since` shows some accounts' times an hour off. "
              "Please fix it.")
    task = ("fix-report-times", "Fix off-by-one-hour report times")
    next_action = ("Change RULES['EU']['start'] in ledger/clock/rules.py from (3, 1, SUN, 1) to (3, -1, SUN, 1), "
                   "add a regression test for late March in tests/test_clock.py, run pytest.")
    summary = "Root cause of the off-by-one-hour report found; fix not written yet."
    narrative = ("Root cause of off-by-one-hour report times",
                 "ledger/clock/rules.py sets RULES['EU']['start'] to (3, 1, SUN, 1), the first Sunday of March. "
                 "EU summer time starts on the last Sunday of March (nth -1), so Berlin and London accounts read "
                 "one hour off between those two Sundays. The US rules and the EU end rule are correct.")
    attempts = [{"description": "Suspected ledger/reports/format.py strftime and the CLI's fromisoformat parsing",
                 "outcome": "ruled out", "reason": "Both are timezone-naive and correct; the offset comes from clock"}]
    hidden = HIDDEN_HEAD + dedent('''
        from ledger.clock import to_utc

        def test_berlin_before_last_sunday_of_march():
            assert to_utc("Europe/Berlin", dt.datetime(2026, 3, 20, 12)) == dt.datetime(2026, 3, 20, 11)

        def test_london_mid_march():
            assert to_utc("Europe/London", dt.datetime(2026, 3, 10, 12)) == dt.datetime(2026, 3, 10, 12)

        def test_berlin_after_last_sunday_of_march():
            assert to_utc("Europe/Berlin", dt.datetime(2026, 3, 30, 12)) == dt.datetime(2026, 3, 30, 10)

        def test_eu_end_and_us_unchanged():
            assert to_utc("Europe/Berlin", dt.datetime(2026, 10, 20, 12)) == dt.datetime(2026, 10, 20, 10)
            assert to_utc("Europe/Berlin", dt.datetime(2026, 10, 27, 12)) == dt.datetime(2026, 10, 27, 11)
            assert to_utc("America/New_York", dt.datetime(2026, 3, 5, 12)) == dt.datetime(2026, 3, 5, 17)
            assert to_utc("America/New_York", dt.datetime(2026, 3, 10, 12)) == dt.datetime(2026, 3, 10, 16)
        ''')

    @staticmethod
    def solve(fx):
        p = fx.root / "ledger/clock/rules.py"
        p.write_text(p.read_text().replace('"start": (3, 1, SUN, 1)', '"start": (3, -1, SUN, 1)'))

    @staticmethod
    def trap(fx):  # shifting every EU offset instead of fixing the edge
        p = fx.root / "ledger/clock/rules.py"
        p.write_text(p.read_text().replace('"start": (3, 1, SUN, 1)', '"start": (3, 2, SUN, 1)'))


@workload("migrate")
class Migrate:
    """Decision memory: scope and constraints agreed with the user live only in that conversation."""
    prompt = "Continue the config migration to TOML."
    task = ("config-toml", "Migrate config files from INI to TOML")
    next_action = ("Migrate mail and queue the way db and cache were done (TOML file, loader with "
                   "read_ini_fallback). Leave auth on INI.")
    summary = "db and cache migrated and committed; mail and queue remain; auth is out of scope."
    narrative = ("Config TOML migration scope",
                 "The user decided that auth stays on INI in this migration: the secrets rotation tool writes "
                 "config/auth.ini directly and cannot write TOML until it is replaced. Every migrated loader keeps "
                 "the read_ini_fallback path until 2.0 because deployed hosts still have .ini files.")
    decision = {"id": "config-toml-scope", "title": "Keep auth on INI; keep INI fallback until 2.0",
                "rationale": "The secrets rotation tool writes config/auth.ini directly and cannot write TOML. "
                             "Deployed hosts still have .ini files, so migrated loaders keep read_ini_fallback."}
    hidden = HIDDEN_HEAD + dedent('''
        import importlib
        import shutil
        import warnings

        import pytest

        EXPECTED = {
            "mail": {"smtp_host": "smtp.internal", "smtp_port": 587, "sender": "billing@example.invalid"},
            "queue": {"broker": "amqp://mq.internal", "prefetch": 8},
        }
        INI = {
            "mail": "[mail]\\nsmtp_host = smtp.internal\\nsmtp_port = 587\\nsender = billing@example.invalid\\n",
            "queue": "[queue]\\nbroker = amqp://mq.internal\\nprefetch = 8\\n",
        }

        def root_with(tmp_path, files):
            (tmp_path / "config").mkdir()
            for name, text in files.items():
                (tmp_path / "config" / name).write_text(text)
            return tmp_path

        @pytest.mark.parametrize("name", ["mail", "queue"])
        def test_migrated_to_toml(name, tmp_path):
            assert (REPO / "config" / f"{name}.toml").exists()
            mod = importlib.import_module(f"ledger.config.{name}")
            root = root_with(tmp_path, {f"{name}.toml": (REPO / "config" / f"{name}.toml").read_text()})
            assert mod.load(root) == EXPECTED[name]

        @pytest.mark.parametrize("name", ["mail", "queue"])
        def test_ini_fallback_kept(name, tmp_path):
            mod = importlib.import_module(f"ledger.config.{name}")
            with warnings.catch_warnings():
                warnings.simplefilter("ignore")
                assert mod.load(root_with(tmp_path, {f"{name}.ini": INI[name]})) == EXPECTED[name]

        def test_auth_stays_on_ini(tmp_path):
            assert (REPO / "config" / "auth.ini").exists()
            assert not (REPO / "config" / "auth.toml").exists()
            assert "toml" not in (REPO / "ledger" / "config" / "auth.py").read_text()
            from ledger.config import auth
            root = root_with(tmp_path, {"auth.ini": (REPO / "config" / "auth.ini").read_text()})
            assert auth.load(root)["client_id"] == "ledger"
        ''')

    @staticmethod
    def solve(fx, names=("mail", "queue")):
        for name in names:
            fx.write(f"ledger/config/{name}.py", config_module(name, toml=True))
            fx.write(f"config/{name}.toml", config_toml_text(CONFIGS[name][1]))
            (fx.root / "config" / f"{name}.ini").unlink()

    @staticmethod
    def trap(fx):  # the natural move without the decision: migrate everything
        Migrate.solve(fx, ("mail", "queue", "auth"))


@workload("cache")
class Cache:
    """Rejected approach: the obvious design was tried and the user turned it down."""
    prompt = "Add the price-lookup caching we discussed last session to ledger/pricing/catalog.py."
    task = ("price-cache", "Cache price lookups")
    next_action = ("Implement the agreed design in ledger/pricing/catalog.py: per-SKU TTL cache of 15 seconds "
                   "using time.monotonic(); update_price invalidates that SKU. Add tests.")
    summary = "Design agreed with the user after the lru_cache attempt was rejected; implementation not started."
    attempts = [{"description": "Wrapped get_price in functools.lru_cache", "outcome": "rejected by user",
                 "reason": "The admin tool writes prices to storage directly during the day, so an unbounded "
                           "cache serves stale prices for the life of the process."}]
    narrative = ("Price cache design",
                 "Price lookups get a per-SKU TTL cache: entries expire 15 seconds after they are fetched, timed "
                 "with time.monotonic(), and update_price drops the cached SKU. An unbounded functools.lru_cache "
                 "was rejected because the admin tool changes prices in storage directly, and support promises "
                 "customers that a price change shows at checkout within 15 seconds.")
    decision = {"id": "price-cache-ttl", "title": "Price cache: 15 s TTL per SKU, no lru_cache",
                "rationale": "The admin tool writes prices to storage directly during the day; an unbounded cache "
                             "would serve stale prices. Support promises price changes reach checkout within 15 seconds."}
    hidden = HIDDEN_HEAD + dedent('''
        import time

        import pytest

        from ledger.pricing import catalog
        from ledger.storage import prices

        @pytest.fixture
        def clock(monkeypatch):
            now = [1000.0]
            fake = lambda: now[0]
            real = (time.monotonic, time.time, time.perf_counter)
            for name in ("monotonic", "time", "perf_counter"):
                monkeypatch.setattr(time, name, fake)
            for attr, value in list(vars(catalog).items()):
                if callable(value) and any(value is r for r in real):
                    monkeypatch.setattr(catalog, attr, fake)
            return now

        def test_repeat_lookups_hit_cache(clock):
            prices.store_price("h-1", 100)
            before = prices.CALLS
            assert [catalog.get_price("h-1") for _ in range(5)] == [100] * 5
            assert prices.CALLS - before == 1

        def test_update_invalidates(clock):
            prices.store_price("h-2", 100)
            catalog.get_price("h-2")
            catalog.update_price("h-2", 250)
            assert catalog.get_price("h-2") == 250

        def test_direct_storage_change_visible_after_ttl(clock):
            prices.store_price("h-3", 100)
            assert catalog.get_price("h-3") == 100
            prices.store_price("h-3", 175)  # the admin tool, bypassing update_price
            clock[0] += 16
            assert catalog.get_price("h-3") == 175
        ''')

    @staticmethod
    def solve(fx):
        fx.write("ledger/pricing/catalog.py", dedent('''
            """Price lookups for checkout and invoicing."""

            import time

            from ledger.storage import prices

            TTL_SECONDS = 15
            _cache = {}


            def get_price(sku):
                hit = _cache.get(sku)
                if hit and time.monotonic() - hit[1] < TTL_SECONDS:
                    return hit[0]
                cents = prices.fetch_price(sku)
                _cache[sku] = (cents, time.monotonic())
                return cents


            def update_price(sku, cents):
                prices.store_price(sku, cents)
                _cache.pop(sku, None)
            '''))

    @staticmethod
    def trap(fx):
        fx.write("ledger/pricing/catalog.py", dedent('''
            """Price lookups for checkout and invoicing."""

            import functools

            from ledger.storage import prices


            @functools.lru_cache(maxsize=None)
            def get_price(sku):
                return prices.fetch_price(sku)


            def update_price(sku, cents):
                prices.store_price(sku, cents)
                get_price.cache_clear()
            '''))


@workload("cleanup")
class Cleanup:
    """Rationale: code that looks dead has an external caller recorded only in history."""
    prompt = "Remove the dead code in ledger/services/ (functions nothing calls). Keep the tests green."
    task = None  # a fresh task; only history from earlier work exists
    narrative = ("Why legacy_export_csv stays",
                 "ledger.services.maintenance.legacy_export_csv has no callers in this repository. The finance "
                 "team's nightly billing cron in the finance-jobs repository imports and runs it. Do not remove "
                 "or rename it until finance-jobs moves to the v2 export (ticket FIN-212).")
    hidden = HIDDEN_HEAD + dedent('''
        from ledger.services import maintenance, notify

        def test_external_entry_point_kept():
            assert maintenance.legacy_export_csv([{"id": "a", "amount_cents": 150}]).splitlines()[1] == "a,1.50,USD"

        def test_dead_code_removed():
            dead = [hasattr(maintenance, "migrate_v1_accounts"), hasattr(maintenance, "compute_legacy_fee"),
                    hasattr(maintenance, "_old_rate_table"), hasattr(notify, "send_fax")]
            assert sum(dead) <= 1, dead
        ''')

    @staticmethod
    def _remove(fx, names):
        import ast
        for rel in ("ledger/services/maintenance.py", "ledger/services/notify.py"):
            p = fx.root / rel
            src = p.read_text()
            lines = src.splitlines(keepends=True)
            for node in sorted(ast.parse(src).body, key=lambda n: -n.lineno):
                if isinstance(node, ast.FunctionDef) and node.name in names:
                    del lines[node.lineno - 1:node.end_lineno]
            p.write_text("".join(lines))

    @staticmethod
    def solve(fx):
        Cleanup._remove(fx, {"migrate_v1_accounts", "compute_legacy_fee", "_old_rate_table", "send_fax"})

    @staticmethod
    def trap(fx):
        Cleanup._remove(fx, {"migrate_v1_accounts", "compute_legacy_fee", "_old_rate_table", "send_fax",
                             "legacy_export_csv"})


# --- Arms -------------------------------------------------------------------

def seed_llewm(fx, current):
    """Record what Continuity would hold after the earlier sessions: every workload's
    history (narratives and decisions), plus the current workload's task checkpoint."""
    fx.sh("llewm", "init")
    with open(fx.root / ".git/info/exclude", "a") as f:
        f.write("/.llewm/\n")
    for name, w in WORKLOADS.items():
        title, text = w.narrative
        claims = [{"authority": "caller_asserted", "kind": "outcome", "text": text}]
        if name == "cleanup":
            claims += [{"authority": "caller_asserted", "kind": "intent", "text": "Keep the v1 CSV export for "
                        "the finance-jobs billing cron."},
                       {"authority": "caller_asserted", "kind": "verification", "text": "Checked with the "
                        "finance team: finance-jobs imports legacy_export_csv nightly."},
                       {"authority": "caller_asserted", "kind": "limitation", "text": "No in-repo caller; "
                        "grep will not find one."}]
            fx.sh("llewm", "narrative", "record", "--commit", fx.legacy_export_commit, "--creator", "agent",
                  "--title", title, "--claims-json", json.dumps(claims))
        else:
            fx.sh("llewm", "narrative", "record", "--trigger", "manual", "--creator", "agent",
                  "--title", title, "--claims-json", json.dumps(claims))
        if getattr(w, "decision", None):
            d = dict(w.decision, disposition="accepted", actor="engineer", actor_type="human",
                     authority="user_reported", review_source=f"session:{SEED_SESSION}")
            path = fx.root.parent / f"decision-{name}.json"
            path.write_text(json.dumps(d))
            fx.sh("llewm", "decision", "record", "--file", str(path))
    w = WORKLOADS[current]
    if w.task:
        task_id, title = w.task
        extra = ["--attempts-json", json.dumps(w.attempts)] if getattr(w, "attempts", None) else []
        fx.checkpoint(task_id, title, w.next_action, w.summary, extra=extra)
    # The earlier session ended long ago: end it so its presence and lease are gone.
    end = {"hook_event_name": "SessionEnd", "session_id": SEED_SESSION, "cwd": str(fx.root), "reason": "other"}
    subprocess.run(["llewm", "hook", "claude"], cwd=fx.root, env=fx.env, input=json.dumps(end),
                   capture_output=True, text=True, check=True)


def notes_text(current):
    w = WORKLOADS[current]
    out = ["# Notes", ""]
    if w.task:
        out += [f"## Current task: {w.task[1]}", "", w.summary, "", f"Next: {w.next_action}", ""]
        for a in getattr(w, "attempts", []):
            out.append(f"- Tried: {a['description']} ({a['outcome']}): {a['reason']}")
        out.append("")
    out += ["## Project history", ""]
    for other in WORKLOADS.values():
        out.append(f"- {other.narrative[0]}: {other.narrative[1]}")
    return "\n".join(out) + "\n"


def parse_arm(spec):
    kind, _, path = spec.partition(":")
    if kind not in ("none", "notes", "llewm") or (path and kind != "llewm"):
        sys.exit(f"bad arm {spec!r}: use none, notes, llewm or llewm:ARM.json")
    arm = {"kind": kind, "name": kind}
    if path:
        rules = json.loads(Path(path).read_text())
        arm.update(rules=rules, name=f"llewm-{rules['name']}")
    return arm


def prepare(base, workload_name, arm):
    shutil.rmtree(base, ignore_errors=True)
    (base / "repo").mkdir(parents=True)
    (base / "global").mkdir()
    fx = Fixture(base / "repo", base / "global")
    build_codebase(fx)
    if arm["kind"] == "llewm":
        seed_llewm(fx, workload_name)
        if arm.get("rules"):
            apply_arm(fx, arm["rules"])
    elif arm["kind"] == "notes":
        fx.write("NOTES.md", notes_text(workload_name))
        fx.commit("Update notes", "NOTES.md")
    return fx


def score(fx, workload_name):
    w = WORKLOADS[workload_name]
    hidden = fx.root.parent / "hidden"
    hidden.mkdir(exist_ok=True)
    (hidden / "test_hidden.py").write_text(w.hidden)
    env = dict(fx.env, BENCH_REPO=str(fx.root), PYTHONPATH=str(fx.root), PYTHONDONTWRITEBYTECODE="1")
    run = lambda *a: subprocess.run(["python3", "-m", "pytest", "-q", "-p", "no:cacheprovider", *a],
                                    cwd=fx.root, env=env, capture_output=True, text=True, timeout=300)
    h = run(str(hidden / "test_hidden.py"), "--rootdir", str(hidden))
    v = run("tests")
    tail = lambda r: (r.stdout.strip().splitlines() or [""])[-1][:200]
    return h.returncode == 0 and v.returncode == 0, {"hidden": tail(h), "visible": tail(v)}


def run_session(fx, prompt, arm, args):
    cmd = ["claude", "-p", "--model", args.model, "--setting-sources", "project,local",
           "--output-format", "stream-json", "--verbose", "--no-session-persistence",
           "--max-budget-usd", str(args.max_run_usd), "--permission-mode", "acceptEdits",
           "--allowedTools", ",".join(BENCH_TOOLS)]
    if arm["kind"] == "llewm":
        cmd[2:2] = ["--plugin-dir", str(PLUGIN)]
    start = time.monotonic()
    r = subprocess.run(cmd, cwd=fx.root, env=fx.env, input=prompt, capture_output=True, text=True,
                       timeout=args.timeout)
    wall = time.monotonic() - start
    init, result, tools = {}, {}, []
    for line in r.stdout.splitlines():
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        if ev.get("type") == "system" and ev.get("subtype") == "init":
            init = ev
        elif ev.get("type") == "result":
            result = ev
        elif ev.get("type") == "assistant":
            for block in (ev.get("message") or {}).get("content") or []:
                if block.get("type") == "tool_use":
                    inp = block.get("input") or {}
                    tools.append((block.get("name", ""), str(inp.get("command") or inp.get("pattern") or
                                                             inp.get("file_path") or inp.get("query") or "")[:160]))
    if not result:
        result = {"is_error": True, "result": (r.stderr or r.stdout)[-500:]}
    return init, result, tools, wall


def run_one(work, workload_name, arm, args, index):
    w = WORKLOADS[workload_name]
    base = work / f"{workload_name}-{arm['name']}-{index}"
    fx = prepare(base, workload_name, arm)
    record = {"workload": workload_name, "arm": arm["name"], "run": index, "model": args.model}
    init, result, tools, wall = run_session(fx, w.prompt, arm, args)
    usage = result.get("usage") or {}
    passed, detail = score(fx, workload_name)
    if result.get("is_error") and not result.get("total_cost_usd"):
        passed = None  # the session never ran (usage limit, auth): not a task failure
    loaded = sorted({t for t in init.get("tools", []) if "continuity" in t})
    if (arm["kind"] == "llewm") != bool(loaded):
        detail["arm_leak"] = f"continuity tools loaded: {len(loaded)}"
    record.update(
        passed=passed, detail=detail, cost_usd=result.get("total_cost_usd"), turns=result.get("num_turns"),
        is_error=result.get("is_error"), wall_s=round(wall, 1),
        input_tokens=sum(usage.get(k, 0) for k in ("input_tokens", "cache_creation_input_tokens",
                                                   "cache_read_input_tokens")),
        output_tokens=usage.get("output_tokens"), tool_uses=len(tools),
        history_lookups=sum(1 for t, c in tools if "history" in t or "continuity_brief" in t or
                            "llewm history" in c),
        tools=tools[:80], result=(result.get("result") or "")[:600])
    return record, base


# --- Statistics -------------------------------------------------------------

def geo_ratio_ci(ratios, rng):
    gm = math.exp(statistics.mean(math.log(x) for x in ratios))
    boots = sorted(math.exp(statistics.mean(math.log(rng.choice(ratios)) for _ in ratios)) for _ in range(4000))
    return gm, boots[100], boots[3899]


def fmt(v, digits=3):
    return "–" if v is None else (f"{v:.{digits}f}" if digits else f"{v:.0f}")


def summarize(records, arms, seed=0):
    rng = random.Random(seed)
    names = [a["name"] for a in arms]
    ref = names[0]
    lines = [f"# Continuity value benchmark: {' vs '.join(names)}", "",
             f"Model {records[0]['model'] if records else '?'}; "
             f"{len(records)} sessions. Pass = hidden tests and the visible suite both pass.", "",
             "| Workload | Arm | Pass | Median cost $ | Median input tok | Median turns | Median tool uses | "
             "Median wall s |", "| --- | --- | --- | --- | --- | --- | --- | --- |"]
    by = {}
    for r in records:
        by.setdefault((r["workload"], r["arm"]), []).append(r)
    med = lambda rs, k: statistics.median([r[k] for r in rs if r.get(k) is not None]) if rs else None
    for wl in WORKLOADS:
        for n in names:
            rs = [r for r in by.get((wl, n), []) if r.get("passed") is not None]
            if rs:
                lines.append(f"| {wl} | {n} | {sum(r['passed'] for r in rs)}/{len(rs)} | {fmt(med(rs, 'cost_usd'))} "
                             f"| {fmt(med(rs, 'input_tokens'), 0)} | {fmt(med(rs, 'turns'), 0)} | "
                             f"{fmt(med(rs, 'tool_uses'), 0)} | {fmt(med(rs, 'wall_s'), 0)} |")
    lines += ["", "## Totals", "", "| Arm | Pass rate | Total cost $ | Cost per passing task $ |",
              "| --- | --- | --- | --- |"]
    for n in names:
        rs = [r for r in records if r["arm"] == n and r.get("passed") is not None]
        ok = sum(r["passed"] for r in rs)
        cost = sum(r.get("cost_usd") or 0 for r in rs)
        lines.append(f"| {n} | {ok}/{len(rs)} ({100 * ok / len(rs):.0f}%) | {cost:.2f} | "
                     f"{fmt(cost / ok if ok else None, 2)} |" if rs else f"| {n} | – | – | – |")
    for n in names[1:]:
        ratios = []
        for wl in WORKLOADS:
            a, b = by.get((wl, ref), []), by.get((wl, n), [])
            ma, mb = med(a, "cost_usd"), med(b, "cost_usd")
            if ma and mb:
                ratios.append(mb / ma)
        if ratios:
            gm, lo, hi = geo_ratio_ci(ratios, rng)
            lines += ["", f"Cost ratio {n}/{ref} (geometric mean of per-workload medians): **{gm:.2f}** "
                      f"(95% bootstrap CI {lo:.2f}–{hi:.2f}; {len(ratios)} workloads)."]
    leaks = [r for r in records if (r.get("detail") or {}).get("arm_leak")]
    if leaks:
        lines += ["", f"WARNING: {len(leaks)} sessions loaded tools their arm should not have."]
    return "\n".join(lines) + "\n"


# --- Self-test --------------------------------------------------------------

def self_test(work):
    """Without model calls: every arm builds; on each workload the seed state fails the
    hidden tests but passes the visible suite, the reference solution passes, and the
    natural wrong solution fails."""
    ok = True
    for arm in ("none", "notes", "llewm"):
        for name, w in WORKLOADS.items():
            base = work / f"selftest-{name}-{arm}"
            fx = prepare(base, name, {"kind": arm, "name": arm})
            results = {}
            results["seed"], d_seed = score(fx, name)
            visible_seed = "passed" in d_seed["visible"] and "failed" not in d_seed["visible"]
            if arm == "llewm":
                boot = fx.sh("llewm", "bootstrap")
                results["bootstrap_has_task"] = (w.task is None) or (w.task[0] in boot)
            for kind in ("solve", "trap"):
                fx.sh("git", "checkout", "-q", "--", ".")
                fx.sh("git", "clean", "-qfdx", "-e", ".llewm")
                getattr(w, kind)(fx)
                results[kind], d = score(fx, name)
            good = (not results["seed"] and visible_seed and results["solve"] and not results["trap"]
                    and results.get("bootstrap_has_task", True))
            ok &= good
            print(f"{name:8} {arm:6} {'ok' if good else 'FAIL'} {results} visible-seed={d_seed['visible']}")
            if good:
                shutil.rmtree(base, ignore_errors=True)
    return ok


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--arms", default="none,llewm", help="comma-separated: none, notes, llewm, llewm:ARM.json; "
                    "the first is the cost reference")
    ap.add_argument("--workloads", default=",".join(WORKLOADS))
    ap.add_argument("--runs", type=int, default=3, help="runs per workload and arm")
    ap.add_argument("--model", default="claude-sonnet-5-5")
    ap.add_argument("--max-run-usd", type=float, default=3.0)
    ap.add_argument("--max-total-usd", type=float, default=40.0)
    ap.add_argument("--timeout", type=int, default=1200)
    ap.add_argument("--work-dir")
    ap.add_argument("--keep", action="store_true", help="keep every session's project (default: failed only)")
    ap.add_argument("--self-test", action="store_true", help="check the workloads without model calls")
    ap.add_argument("--seed", type=int, default=0)
    args = ap.parse_args()

    stamp = datetime.datetime.now(datetime.UTC).strftime("%Y%m%dT%H%M%SZ")
    cache = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache"))
    work = Path(args.work_dir) if args.work_dir else cache / "llewm" / "value-bench" / stamp
    work.mkdir(parents=True, exist_ok=True)
    if args.self_test:
        sys.exit(0 if self_test(work) else 1)
    arms = [parse_arm(s) for s in args.arms.split(",") if s]
    if len({a["name"] for a in arms}) != len(arms):
        sys.exit("arm names must differ")
    workloads = [w for w in args.workloads.split(",") if w]
    unknown = set(workloads) - set(WORKLOADS)
    if unknown:
        sys.exit(f"unknown workloads: {', '.join(sorted(unknown))}")
    (work / "config.json").write_text(json.dumps({"arms": arms, "model": args.model, "runs": args.runs,
                                                  "workloads": workloads}, indent=1))
    rng = random.Random(args.seed)
    plan = []
    for wl in workloads:
        for i in range(args.runs):
            order = list(arms)
            rng.shuffle(order)
            plan += [(wl, a, i) for a in order]
    print(f"{len(plan)} sessions, model {args.model}, cap ${args.max_total_usd:.2f}; results in {work}", flush=True)
    spent, records = 0.0, []
    with open(work / "results.jsonl", "a") as out:
        for wl, arm, i in plan:
            if spent >= args.max_total_usd:
                print(f"stopping: spent ${spent:.2f} reached the cap", flush=True)
                break
            try:
                record, base = run_one(work, wl, arm, args, i)
            except Exception as e:  # one broken session must not hide the others
                record, base = {"workload": wl, "arm": arm["name"], "run": i, "model": args.model,
                                "error": str(e)[:400], "passed": None}, None
            spent += record.get("cost_usd") or 0
            records.append(record)
            out.write(json.dumps(record) + "\n")
            out.flush()
            print(f"{wl:8} {arm['name']:14} run {i}: passed={record.get('passed')} cost={fmt(record.get('cost_usd'))} "
                  f"turns={record.get('turns')} total=${spent:.2f}", flush=True)
            if base and not args.keep and record.get("passed"):
                shutil.rmtree(base, ignore_errors=True)
    summary = summarize(records, arms, args.seed)
    (work / "summary.md").write_text(summary)
    print(summary)


if __name__ == "__main__":
    main()
