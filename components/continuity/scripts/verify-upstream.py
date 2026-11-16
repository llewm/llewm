"""Developer/CI checks and an exact-commit receipt. Never called by installation.

Run from a clean checkout; put --output outside it. Logs stay in the CI run,
while the portable receipt contains only identities and result states.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[3]
COMPONENTS = ("continuity", "action-control", "orchestration", "privatestorage")


def git(*args):
    env = {k: v for k, v in os.environ.items() if not k.upper().startswith("GIT_")}
    return subprocess.check_output(["git", "-C", str(ROOT), *args], env=env,
                                   text=True).strip()


def checks():
    for component in COMPONENTS:
        for name, command in (
            ("test", ["go", "test", "./...", "-count=1", "-timeout=40m", "-p=2"]),
            ("vet", ["go", "vet", "./..."]),
            ("fmt", ["gofmt", "-l", "."]),
            ("build", ["go", "build", "./..."]),
            ("standalone-build", ["go", "build", "./..."]),
        ):
            yield "components/" + component, name, command
    yield "components/continuity", "python-tests", [
        sys.executable, "-B", "-m", "unittest", "discover", "-s", "scripts", "-p", "*_test.py"]


def execute(component, name, command):
    env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
    if name == "standalone-build":
        env["GOWORK"] = "off"
    print(f"{component}: {name}", flush=True)
    try:
        result = subprocess.run(command, cwd=ROOT / component, env=env,
                                stdout=subprocess.PIPE if name == "fmt" else None,
                                text=True, check=False)
        if name == "fmt" and result.stdout:
            print(result.stdout, flush=True)
        return "passed" if result.returncode == 0 and (name != "fmt" or not result.stdout) else "failed"
    except OSError:
        # Do not place machine paths or environment values in the receipt.
        return "failed"


def verify(output, repository, run_url):
    output = output.resolve()
    if output == ROOT or ROOT in output.parents:
        raise ValueError("receipt output must be outside the source checkout")
    if git("status", "--porcelain"):
        raise ValueError("verification requires a clean checkout")
    commit = git("rev-parse", "HEAD")
    info = json.loads(subprocess.check_output(
        ["go", "env", "-json", "GOOS", "GOARCH", "GOHOSTOS", "GOHOSTARCH", "GOVERSION"],
        cwd=ROOT, text=True))
    if (info["GOOS"], info["GOARCH"]) != (info["GOHOSTOS"], info["GOHOSTARCH"]):
        raise ValueError("cross-compilation cannot establish native test evidence")
    receipt = dict(schema_version=1, repository=repository, commit=commit,
                   target=info["GOOS"] + "/" + info["GOARCH"],
                   go_version=info["GOVERSION"], run_url=run_url,
                   status="pending", checks=[])
    # A killed/timed-out run leaves pending evidence, never a stale success.
    output.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    for component, name, command in checks():
        receipt["checks"].append(dict(component=component, name=name,
                                      status=execute(component, name, command)))
        output.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    unchanged = git("rev-parse", "HEAD") == commit and not git("status", "--porcelain")
    receipt["status"] = "passed" if unchanged and all(
        check["status"] == "passed" for check in receipt["checks"]) else "failed"
    output.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    return 0 if receipt["status"] == "passed" else 1


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--run-url", required=True)
    args = parser.parse_args()
    sys.exit(verify(args.output, args.repository, args.run_url))
