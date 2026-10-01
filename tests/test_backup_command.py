"""Building the proxmox-backup-client command and environment."""
import unittest

from tests.helpers import FINGERPRINT, app


def job(**over):
    base = {"shares": [{"path": "/srv/d/media", "archive": "media"}, {"path": "/srv/d/docs", "archive": "docs"}],
            "backup_id": "omv-media", "change_detection_mode": "metadata", "rate": "", "excludes": [],
            "keyfile": "", "keyfile_password": ""}
    base.update(over)
    return base


TARGET = {"host": "pbs.lan", "port": 8007, "datastore": "store", "username": "omv@pbs",
          "token_name": "omv", "secret": "tok", "fingerprint": FINGERPRINT}


class CommandTests(unittest.TestCase):
    def test_basic_command(self):
        cmd = app.build_backup_cmd(job())
        self.assertEqual(cmd[1:], ["backup", "media.pxar:/srv/d/media", "docs.pxar:/srv/d/docs",
                                   "--backup-id", "omv-media", "--change-detection-mode", "metadata"])

    def test_legacy_mode_omits_flag(self):
        self.assertNotIn("--change-detection-mode", app.build_backup_cmd(job(change_detection_mode="legacy")))

    def test_options(self):
        cmd = app.build_backup_cmd(job(rate="20 MiB", excludes=["/Movies", "**/.recycle"], keyfile="/root/k.json"))
        self.assertIn("--rate", cmd)
        self.assertEqual(cmd[cmd.index("--rate") + 1], "20MiB")
        self.assertEqual([cmd[i + 1] for i, a in enumerate(cmd) if a == "--exclude"], ["/Movies", "**/.recycle"])
        self.assertEqual(cmd[cmd.index("--keyfile") + 1], "/root/k.json")

    def test_environment(self):
        env = app.client_env(TARGET, job(keyfile_password="kp"))
        self.assertEqual(env["PBS_REPOSITORY"], "omv@pbs!omv@pbs.lan:8007:store")
        self.assertEqual(env["PBS_PASSWORD"], "tok")
        self.assertEqual(env["PBS_FINGERPRINT"], FINGERPRINT)
        self.assertEqual(env["PBS_ENCRYPTION_PASSWORD"], "kp")

    def test_environment_without_fingerprint(self):
        env = app.client_env(dict(TARGET, fingerprint=""))
        self.assertNotIn("PBS_FINGERPRINT", env)
        self.assertNotIn("PBS_ENCRYPTION_PASSWORD", env)


class HelperTests(unittest.TestCase):
    def test_summarize_error_prefers_error_lines(self):
        text = "Starting\nupload 50%\nError: connection reset\nfinal line\n"
        self.assertEqual(app.summarize_error(text), "Error: connection reset")
        self.assertEqual(app.summarize_error("just output\nlast"), "last")
        self.assertTrue(app.summarize_error(""))

    def test_archive_from_path(self):
        self.assertEqual(app.archive_from_path("/srv/disk/Media Files"), "Media-Files")
        self.assertEqual(app.archive_from_path("/"), "root")
        self.assertEqual(app.archive_from_path("/srv/.hidden"), "hidden")
        self.assertEqual(app.archive_from_path("/srv/-x-"), "x")

    def test_top_level_paths(self):
        self.assertEqual(app.top_level_paths(["/a/b", "/a", "/c", "/ab", "/a/b/c", "/c"]), ["/a", "/ab", "/c"])

    def test_base_path_and_ips(self):
        self.assertEqual(app.normalize_base_path("backups/"), "/backups")
        self.assertEqual(app.normalize_base_path("/"), "")
        self.assertEqual(str(app.parse_ip("::ffff:10.0.0.1")), "10.0.0.1")
        self.assertIsNone(app.parse_ip("not-an-ip"))
        nets = app.parse_networks(["127.0.0.1", "172.16.0.0/12", "junk"])
        self.assertEqual(len(nets), 2)


if __name__ == "__main__":
    unittest.main()
