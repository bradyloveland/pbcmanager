"""The helpers the release workflow uses to check versions, write notes and package."""
import os
import re
import sys
import tarfile
import tempfile
import unittest

from tests.helpers import ROOT, app

sys.path.insert(0, os.path.join(ROOT, "scripts"))
import release_tools  # noqa: E402


class ReleaseToolsTests(unittest.TestCase):
    def test_version_matches_app(self):
        self.assertEqual(release_tools.version(), app.VERSION)

    def test_changelog_has_current_version(self):
        notes = release_tools.notes(app.VERSION)
        self.assertIn("### Added", notes)
        self.assertNotIn(f"## [{app.VERSION}]", notes)  # heading itself excluded
        self.assertNotIn("## 1.1.0", notes)              # stops at the next section
        self.assertIn(f"pbswebclient-{app.VERSION}.tar.gz", notes)

    def test_missing_version_exits(self):
        with self.assertRaises(SystemExit):
            release_tools.notes("0.0.0")

    def test_package_contents(self):
        out = tempfile.mkdtemp()
        release_tools.package("9.9.9", out)
        path = os.path.join(out, "pbswebclient-9.9.9.tar.gz")
        with tarfile.open(path) as tar:
            names = tar.getnames()
            owners = {(m.uid, m.gid) for m in tar.getmembers()}
        for required in ("app.py", "qr.py", "static/index.html", "install.sh", "uninstall.sh", "LICENSE"):
            self.assertIn(f"pbswebclient-9.9.9/{required}", names)
        self.assertFalse(any("__pycache__" in n or n.endswith(".pyc") for n in names))
        self.assertFalse(any("/tests/" in n or "/.git" in n for n in names))
        self.assertEqual(owners, {(0, 0)})
        with open(path + ".sha256") as f:
            self.assertRegex(f.read(), r"^[0-9a-f]{64}  pbswebclient-9\.9\.9\.tar\.gz\n$")

    def test_installer_copies_every_runtime_file(self):
        with open(os.path.join(ROOT, "install.sh")) as f:
            installer = f.read()
        for name in ("app.py", "qr.py", "static/index.html"):
            self.assertIn(f'"$SRC_DIR/{name}"', installer)
        self.assertTrue(re.search(r"^VERSION = \"\d+\.\d+\.\d+\"$", open(os.path.join(ROOT, "app.py")).read(), re.M))


if __name__ == "__main__":
    unittest.main()
