"""Exercise the shipped hook commands in disposable, unconfigured directories."""
import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import statistics
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="llewm", help="candidate executable or PATH name")
    parser.add_argument("--plugins", type=Path, default=Path(__file__).resolve().parents[1] / "plugins")
    args = parser.parse_args()
    executable = shutil.which(args.binary)
    if not executable:
        parser.error("cannot find candidate executable")
    binary = Path(executable).resolve()
    commands = []
    for name, client in (("claude-code", "claude-code"), ("llewm", "codex")):
        manifest = json.loads((args.plugins / name / "hooks/hooks.json").read_text(encoding="utf-8"))
        for event, groups in manifest["hooks"].items():
            for group in groups:
                for hook in group["hooks"]:
                    if hook.get("statusMessage") or hook["command"] != f"llewm hook {client}":
                        raise SystemExit(f"unexpected hook command or status banner: {client} {event}")
                    commands.append((client, event, hook["timeout"]))

    env = os.environ.copy()
    env.pop("CONTINUITY_HOME", None)
    with tempfile.TemporaryDirectory(prefix="llewm-idle-hooks-") as tmp:
        root = Path(tmp)
        if any((p / marker).exists() for p in root.parents
               for marker in (".llewm", ".continuity", ".git")):
            # A temp folder inside a managed checkout can inherit real project state.
            parser.error("use a temp directory outside all Git and Llewm projects")
        plain = root / "plain"
        plain.mkdir()
        collection = root / "collection"
        repo = collection / "first"
        for path in (repo, collection / "second"):
            subprocess.run(["git", "init", "-q", str(path)], check=True)
        nested = repo / "nested"
        nested.mkdir()
        env["CONTINUITY_GLOBAL_HOME"] = str(root / "global")
        before = set(root.rglob("*"))
        timings = []
        for cwd in (plain, repo, nested, collection):
            for client, event, timeout in commands:
                body = json.dumps({"hook_event_name": event, "session_id": "idle-qual", "cwd": str(cwd)})
                start = time.perf_counter()
                run = subprocess.run([str(binary), "hook", client], input=body.encode(),
                                     cwd=cwd, env=env, capture_output=True, timeout=timeout)
                timings.append((time.perf_counter() - start) * 1000)
                if run.returncode or run.stdout or run.stderr:
                    raise SystemExit(f"{client} {event} in {cwd.name}: exit={run.returncode}, "
                                     f"stdout={len(run.stdout)} bytes, stderr={len(run.stderr)} bytes")
        if before != set(root.rglob("*")):
            raise SystemExit("unconfigured hooks changed the disposable directory tree")
        print(f"PASS {platform.system()}/{platform.machine()}: {len(timings)} hook processes, "
              f"median {statistics.median(timings):.1f} ms, max {max(timings):.1f} ms; "
              "empty stdout/stderr, exit 0, unchanged directory inventory")


if __name__ == "__main__":
    main()
