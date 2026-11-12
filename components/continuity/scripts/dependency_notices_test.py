import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    "dependency_notices", Path(__file__).with_name("dependency-notices.py"))
notices = importlib.util.module_from_spec(spec)
spec.loader.exec_module(notices)


class DependencyNoticesTest(unittest.TestCase):
    def test_reviewed_repository_library_uses_root_license(self):
        name = "github.com/llewm/llewm/components/privatestorage"
        directory = notices.ROOT / "components/privatestorage"
        self.assertIsNone(notices.dependency_directory({
            "Path": name, "Dir": str(directory),
            "Replace": {"Path": "../privatestorage", "Dir": str(directory)},
        }))

    def test_unreviewed_replacements_are_refused(self):
        name = "github.com/llewm/llewm/components/privatestorage"
        directory = notices.ROOT / "components/privatestorage"
        for module in (
            {"Path": "example.com/foreign", "Dir": str(directory),
             "Replace": {"Dir": str(directory)}},
            {"Path": name, "Dir": str(directory.parent / "other"),
             "Replace": {"Dir": str(directory.parent / "other")}},
            {"Path": name, "Dir": str(directory),
             "Replace": {"Dir": str(directory.parent / "other")}},
            {"Path": name, "Dir": str(directory),
             "Replace": {"Path": "example.com/replacement", "Version": "v1.0.0"}},
        ):
            with self.subTest(module=module), self.assertRaises(SystemExit):
                notices.dependency_directory(module)

    def test_external_dependencies_still_collect_notices(self):
        directory = notices.ROOT / "external-module-fixture"
        self.assertEqual(notices.dependency_directory({
            "Path": "golang.org/x/sys", "Version": "v0.41.0",
            "Dir": str(directory),
        }), directory)


if __name__ == "__main__":
    unittest.main()
