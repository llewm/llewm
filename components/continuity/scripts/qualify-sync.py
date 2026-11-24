"""Run the deterministic sync baseline and qualify detached candidate processes."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile
import time

MODULE = Path(__file__).resolve().parents[1]
TESTS = [
    "TestBlockCycleConvergesAcrossGitPeers", "TestSyncKeepsLocalCommitsWhileOffline",
    "TestConcurrentRevisionsBecomeAForkInsteadOfACollision", "TestCrossMachineLeasesThroughGitRefs",
    "TestUnchangedSyncInvalidationAndRecovery", "TestAutomaticSyncTriggers",
]


class Failure(Exception):
    """Only synthetic labels and numeric outcomes may enter the receipt."""


def run(label, argv, cwd, env=None, body=None, timeout=30):
    try:
        proc = subprocess.run(argv, cwd=cwd, env=env, input=body or b"", capture_output=True,
                              timeout=timeout)
    except subprocess.TimeoutExpired:
        raise Failure(f"{label}: timed out after {timeout}s") from None
    if proc.returncode:
        raise Failure(f"{label}: exit {proc.returncode}")
    return proc.stdout


def baseline():
    pattern = "^(" + "|".join(TESTS) + ")$"
    events = run("Go regression tests", ["go", "test", "-json", "-count=1", "-timeout=40m",
                 "-run", pattern, "./internal/memsync", "./cmd/llewm"], MODULE, timeout=600)
    passed = {event.get("Test") for event in map(json.loads, events.splitlines())
              if event.get("Action") == "pass"}
    missing = set(TESTS) - passed
    if missing:
        raise Failure("missing passing tests: " + ", ".join(sorted(missing)))


class Peer:
    def __init__(self, binary, home):
        self.binary = binary
        self.repo = home / "project"
        self.repo.mkdir(parents=True)
        self.env = {k: v for k, v in os.environ.items() if k.upper() in
                    {"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "TMP", "TEMP", "TMPDIR",
                     "COMSPEC", "SYSTEMDRIVE", "LANG"}}
        self.env.update(HOME=str(home), USERPROFILE=str(home), APPDATA=str(home / "config"),
                        LOCALAPPDATA=str(home / "data"), XDG_DATA_HOME=str(home / "data"),
                        CONTINUITY_GLOBAL_HOME=str(home / "global"), GIT_CONFIG_NOSYSTEM="1",
                        GIT_CONFIG_GLOBAL=os.devnull, GIT_TERMINAL_PROMPT="0",
                        GIT_ALLOW_PROTOCOL="file", GIT_AUTHOR_NAME="Qualification",
                        GIT_AUTHOR_EMAIL="qualification@example.invalid",
                        GIT_COMMITTER_NAME="Qualification", GIT_COMMITTER_EMAIL="qualification@example.invalid")
        self.git("init", "-q")
        self.git("commit", "-q", "--allow-empty", "-m", "Synthetic project")
        self.cli("init")

    def git(self, *args):
        return run("git " + args[0], ["git", *args], self.repo, self.env)

    def cli(self, *args, body=None):
        return run("llewm " + " ".join(args[:2]), [self.binary, *args], self.repo, self.env, body)

    def start(self, session):
        body = json.dumps(dict(session_id=session, hook_event_name="SessionStart",
                               source="startup", cwd=str(self.repo))).encode()
        self.cli("hook", "claude-code", body=body)

    def await_sync(self, previous=None):
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            latest = json.loads(self.cli("sync", "status", "--output", "json")).get("latest")
            if latest and latest["id"] != previous:
                if latest["outcome"] != "synced" or any(latest[key] for key in
                        ("collisions", "forks", "supersession_forks", "unpushed_commits", "deferred_records")):
                    raise Failure("expected a clean successful sync attempt")
                return latest
            time.sleep(0.2)
        raise Failure("no fresh completed sync attempt within 45s")


def smoke(binary, root, report):
    a = Peer(binary, root / "a")
    b = Peer(binary, root / "b")
    remote = str(root / "memory.git")
    a.git("init", "-q", "--bare", remote)
    lineage = json.loads(a.cli("sync", "init"))["lineage_id"]
    for peer in (a, b):
        peer.cli("sync", "init", "--lineage", lineage, "--remote", remote)
    summary = "Synthetic delivery sample 37"
    report["stage"] = "checkpoint publication"
    checkpoint = json.loads(a.cli("checkpoint", "create", "--task-id", "qualify-sync",
                            "--task", "Qualify sync", "--next", "Recover sample", "--summary",
                            summary, "--output", "json"))["id"]
    publication = a.await_sync()
    report["published_records"] = publication["exported_records"]
    if publication["exported_records"] < 1:
        raise Failure("publisher exported zero records")
    export = ("checkpoint", "show", checkpoint, "--output", "json")
    expected = a.cli(*export)
    source = json.loads(expected)
    if source["id"] != checkpoint or source["truncated"] or summary not in source["markdown"]:
        raise Failure("source checkpoint export missing expected ID or summary")
    report["stage"] = "startup import"
    b.start("qual-start")
    first = b.await_sync()
    report["imported_records"] = first["imported_records"]
    if first["imported_records"] < 1 or b.cli(*export) != expected:
        raise Failure("expected imported records and byte-identical checkpoint export")
    report["stage"] = "idle restart"
    head = a.git("--git-dir", remote, "rev-parse", "HEAD")
    b.start("qual-restart")
    idle = b.await_sync(first["id"])
    if idle["imported_records"] or idle["exported_records"] or head != a.git("--git-dir", remote, "rev-parse", "HEAD"):
        raise Failure("expected zero transferred records and unchanged remote HEAD")
    report["idle_imports"] = idle["imported_records"]
    report["idle_exports"] = idle["exported_records"]
    recovered = json.loads(b.cli("bootstrap", "--output", "json")).get("checkpoint") or {}
    if recovered.get("id") != checkpoint or recovered.get("summary") != summary:
        raise Failure("fresh bootstrap did not select the published checkpoint")
    report["checkpoint_export_sha256"] = hashlib.sha256(expected).hexdigest()
    if b.cli(*export) != expected:
        raise Failure("checkpoint bytes changed after restart")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, help="write the redacted JSON receipt here")
    args = parser.parse_args()
    report = dict(platform=platform.system(), architecture=platform.machine(), stage="baseline",
                  expected_tests=TESTS, status="failed")
    try:
        report["stage"] = "build candidate"
        with tempfile.TemporaryDirectory(prefix="llewm-sync-qual-") as tmp:
            root = Path(tmp)
            binary = root / ("llewm.exe" if os.name == "nt" else "llewm")
            run("go build", ["go", "build", "-o", binary, "./cmd/llewm"], MODULE, timeout=600)
            version = json.loads(run("version", [binary, "version", "--output", "json"], root))
            report.update({k: version[k] for k in ("git_commit", "build_dirty", "go_version")})
            report["stage"] = "baseline"
            baseline()
            report["stage"] = "initialize synthetic peers"
            smoke(binary, root, report)
        report.update(stage="complete", status="passed")
    except Failure as exc:
        report["error"] = str(exc)
    except (ValueError, OSError, KeyError) as exc:
        report["error"] = type(exc).__name__
    receipt = json.dumps(report, indent=2)
    print(receipt)
    if args.output:
        args.output.write_text(receipt + "\n", encoding="utf-8")
    return int(report["status"] != "passed")


if __name__ == "__main__":
    sys.exit(main())
