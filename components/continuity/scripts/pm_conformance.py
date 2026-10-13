#!/usr/bin/env python3
"""Exercise the public CLI with synthetic files; requires only Python's stdlib.

This is a developer test runner, not an adapter host or a Continuity command.
"""
import argparse
import copy
import json
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, help="path to a built continuity executable")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve(strict=True))
    fixtures = Path(__file__).resolve().parents[1] / "testdata" / "pm-adapter"
    cases = json.loads((fixtures / "cases.json").read_text())
    passed = 0
    with tempfile.TemporaryDirectory(prefix="pm-conformance-") as scratch:
        root = Path(scratch)

        def run(data, valid=True, code=None, project="project"):
            nonlocal passed
            packet = root / "input.json"
            packet.write_bytes(data)
            command = [binary, "--project-root", str(root / "absent"), "pm", "inspect",
                       "--file", str(packet), "--provider", "example",
                       "--instance", "tenant", "--project", project]
            first = subprocess.run(command, cwd=root, capture_output=True, timeout=15)
            second = subprocess.run(command, cwd=root, capture_output=True, timeout=15)
            assert (first.returncode, first.stdout, first.stderr) == (second.returncode, second.stdout, second.stderr), "unstable replay"
            assert list(root.iterdir()) == [packet], "unexpected filesystem write"
            assert packet.read_bytes() == data, "input modified"
            assert b"DO_NOT_PRINT" not in first.stdout + first.stderr, "secret echoed"
            if valid:
                assert first.returncode == 0 and not first.stderr, "valid input rejected"
                report = json.loads(first.stdout)
                assert report["state"] == "valid" and len(report["content_sha256"]) == 64
                passed += 1
                return report
            assert first.returncode != 0 and not first.stdout, "invalid input accepted/emitted"
            if code:
                assert code.encode() in first.stderr, "unexpected rejection code"
            passed += 1

        for case in cases:
            report = run((fixtures / case["file"]).read_bytes(), case["valid"], case.get("error"))
            if report is not None:
                assert [d["code"] for d in report["diagnostics"]] == case["codes"]

        base = json.loads((fixtures / "paginated.json").read_text())
        original = run(json.dumps(base).encode())
        shuffled = copy.deepcopy(base)
        shuffled["snapshot"]["resources"].reverse()
        shuffled["manifest"]["capabilities"].reverse()
        assert original == run(json.dumps(shuffled, sort_keys=True).encode()), "order affects normalized result"

        def changed(edit, code):
            value = copy.deepcopy(base)
            edit(value)
            run(json.dumps(value).encode(), False, code)

        changed(lambda p: p.update(schema_version=2), "unsupported_version")
        changed(lambda p: p["manifest"].update(capabilities=["read_snapshot"]), "missing_capability")
        changed(lambda p: p["proposals"][0].update(source_revision="r1"), "stale_revision")
        changed(lambda p: p["proposals"][0]["mappings"].append(p["proposals"][0]["mappings"][0]), "conflicting_mapping")
        changed(lambda p: p["snapshot"]["resources"].append(p["snapshot"]["resources"][0]), "duplicate_identity")
        changed(lambda p: p["snapshot"].update(complete=True), "complete_with_cursor")
        changed(lambda p: p.update(unrecognized="DO_NOT_PRINT"), "invalid_schema")
        run(json.dumps(base).encode(), False, "project_mismatch", project="other")
        run(b" " * ((1 << 20) + 1), False)
        run(b"[" * 18 + b"1" + b"]" * 18, False, "invalid_json_structure")
        run(b'{"x":"' + b"x" * 16385 + b'"}', False, "invalid_json_structure")
    print(f"PASS: {passed} public CLI conformance cases (including deterministic replay)")


if __name__ == "__main__":
    main()
