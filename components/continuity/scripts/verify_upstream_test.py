import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace


spec = importlib.util.spec_from_file_location(
    "verify_upstream", Path(__file__).with_name("verify-upstream.py"))
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class VerificationTest(unittest.TestCase):
    def test_command_failure_and_unformatted_output_fail(self):
        for name, code, stdout, expected in (
            ("test", 0, None, "passed"),
            ("test", 1, None, "failed"),
            ("fmt", 0, "", "passed"),
            ("fmt", 0, "unformatted.go\n", "failed"),
            ("fmt", 1, "", "failed"),
        ):
            with self.subTest(name=name, code=code, stdout=stdout), \
                 patch.object(verify.subprocess, "run", return_value=SimpleNamespace(returncode=code, stdout=stdout)):
                self.assertEqual(verify.execute("components/continuity", name, ["fixture"]), expected)

    def test_dirty_source_refused_before_checks(self):
        with tempfile.TemporaryDirectory() as tmp, \
             patch.object(verify, "git", return_value=" M source.go"), \
             patch.object(verify, "execute") as execute:
            with self.assertRaisesRegex(ValueError, "clean checkout"):
                verify.verify(Path(tmp) / "receipt.json", "https://github.com/llewm/llewm", "https://example.org/runs/1")
            execute.assert_not_called()

    def test_receipt_failure_and_source_change_cannot_pass(self):
        for fail, changed in ((False, False), (True, False), (False, True)):
            with self.subTest(fail=fail, changed=changed), tempfile.TemporaryDirectory() as tmp:
                output = Path(tmp) / "receipt.json"
                def fake_git(*args):
                    if args[0] == "status":
                        return ""
                    fake_git.calls += 1
                    return ("b" if changed and fake_git.calls > 1 else "a") * 40
                fake_git.calls = 0
                env = json.dumps(dict(GOOS="windows", GOARCH="amd64", GOHOSTOS="windows",
                                      GOHOSTARCH="amd64", GOVERSION="go1.25.0"))
                with patch.object(verify, "git", side_effect=fake_git), \
                     patch.object(verify.subprocess, "check_output", return_value=env), \
                     patch.object(verify, "execute", return_value="failed" if fail else "passed"):
                    code = verify.verify(output, "https://github.com/llewm/llewm", "https://example.org/runs/1")
                receipt = json.loads(output.read_text())
                self.assertEqual(code, int(fail or changed))
                self.assertEqual(receipt["status"], "failed" if fail or changed else "passed")
                self.assertEqual(receipt["commit"], "a" * 40)
                self.assertEqual(len(receipt["checks"]), 21)

    def test_interruption_leaves_pending_receipt(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "receipt.json"
            env = json.dumps(dict(GOOS="linux", GOARCH="amd64", GOHOSTOS="linux",
                                  GOHOSTARCH="amd64", GOVERSION="go1.25.0"))
            with patch.object(verify, "git", side_effect=["", "a" * 40]), \
                 patch.object(verify.subprocess, "check_output", return_value=env), \
                 patch.object(verify, "execute", side_effect=KeyboardInterrupt):
                with self.assertRaises(KeyboardInterrupt):
                    verify.verify(output, "https://github.com/llewm/llewm", "https://example.org/runs/1")
            self.assertEqual(json.loads(output.read_text())["status"], "pending")

    def test_cross_target_refused_before_checks(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = json.dumps(dict(GOOS="windows", GOARCH="amd64", GOHOSTOS="linux",
                                  GOHOSTARCH="amd64", GOVERSION="go1.25.0"))
            with patch.object(verify, "git", side_effect=["", "a" * 40]), \
                 patch.object(verify.subprocess, "check_output", return_value=env), \
                 patch.object(verify, "execute") as execute:
                with self.assertRaisesRegex(ValueError, "cross-compilation"):
                    verify.verify(Path(tmp) / "receipt.json", "https://github.com/llewm/llewm", "https://example.org/runs/1")
                execute.assert_not_called()


if __name__ == "__main__":
    unittest.main()
