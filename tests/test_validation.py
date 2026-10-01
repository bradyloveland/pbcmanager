"""Input validation for destinations, jobs, schedules and email settings."""
import unittest

from tests.helpers import FINGERPRINT, app, fresh_dirs

ApiError = app.ApiError


def target(**over):
    base = {"name": "Home PBS", "host": "192.0.2.10", "port": 8007, "datastore": "backup-pool",
            "username": "omv@pbs", "token_name": "omv", "secret": "s3cr3t", "fingerprint": FINGERPRINT}
    base.update(over)
    return base


class TargetValidationTests(unittest.TestCase):
    def test_valid(self):
        t = app.clean_target(target())
        self.assertEqual(t["port"], 8007)
        self.assertEqual(len(t["id"]), 12)
        self.assertEqual(app.repository(t), "omv@pbs!omv@192.0.2.10:8007:backup-pool")

    def test_repository_without_token_and_ipv6(self):
        t = app.clean_target(target(host="fd00::5", token_name=""))
        self.assertEqual(app.repository(t), "omv@pbs@[fd00::5]:8007:backup-pool")

    def test_rejects_bad_fields(self):
        cases = {
            "host": "bad host!", "datastore": "has space", "username": "no-realm",
            "token_name": "bad!token", "fingerprint": "ab:cd", "port": 70000, "name": "",
        }
        for field, value in cases.items():
            with self.subTest(field=field), self.assertRaises(ApiError):
                app.clean_target(target(**{field: value}))

    def test_fingerprint_is_lowercased(self):
        self.assertEqual(app.clean_target(target(fingerprint=FINGERPRINT.upper()))["fingerprint"], FINGERPRINT)

    def test_secret_required_unless_kept_or_waived(self):
        with self.assertRaises(ApiError):
            app.clean_target(target(secret=""))
        kept = app.clean_target(target(secret=""), {"id": "abc", "secret": "old"})
        self.assertEqual(kept["secret"], "old")
        self.assertEqual(kept["id"], "abc")
        waived = app.clean_target(target(secret=""), require_secret=False)
        self.assertEqual(waived["secret"], "")

    def test_public_view_hides_secret(self):
        out = app.public_target(app.clean_target(target()))
        self.assertNotIn("secret", out)
        self.assertTrue(out["secret_set"])


class JobValidationTests(unittest.TestCase):
    def setUp(self):
        fresh_dirs()
        self.cfg = app.Config(app.CONFIG_PATH)
        self.t = app.clean_target(target())
        self.cfg.data["targets"].append(self.t)

    def job(self, **over):
        base = {"name": "media", "target_id": self.t["id"], "backup_id": "omv-media",
                "shares": [{"path": "/srv/disk/media"}], "schedule": {"type": "daily", "time": "02:00"}}
        base.update(over)
        return base

    def test_valid_defaults(self):
        j = app.clean_job(self.job(), self.cfg)
        self.assertEqual(j["shares"], [{"path": "/srv/disk/media", "archive": "media"}])
        self.assertEqual(j["change_detection_mode"], "metadata")
        self.assertEqual(j["schedule"]["days"], list(range(7)))
        self.assertTrue(j["enabled"])

    def test_paths_normalized_and_archive_derived(self):
        j = app.clean_job(self.job(shares=[{"path": "/srv/disk//My Movies/"}]), self.cfg)
        self.assertEqual(j["shares"][0], {"path": "/srv/disk/My Movies", "archive": "My-Movies"})

    def test_rejects_relative_path_and_duplicate_archives(self):
        with self.assertRaises(ApiError):
            app.clean_job(self.job(shares=[{"path": "srv/media"}]), self.cfg)
        with self.assertRaises(ApiError):
            app.clean_job(self.job(shares=[{"path": "/a/media"}, {"path": "/b/media"}]), self.cfg)
        with self.assertRaises(ApiError):
            app.clean_job(self.job(shares=[]), self.cfg)

    def test_unknown_target(self):
        with self.assertRaises(ApiError):
            app.clean_job(self.job(target_id="nope"), self.cfg)
        j = app.clean_job(self.job(target_id="other"), None, target_ids={"other"})
        self.assertEqual(j["target_id"], "other")

    def test_excludes_from_text(self):
        j = app.clean_job(self.job(excludes="/Movies\n\n  /TV  \n**/.recycle"), self.cfg)
        self.assertEqual(j["excludes"], ["/Movies", "/TV", "**/.recycle"])

    def test_rate_and_mode(self):
        self.assertEqual(app.clean_job(self.job(rate="20MiB"), self.cfg)["rate"], "20MiB")
        for bad in ({"rate": "fast"}, {"change_detection_mode": "turbo"}, {"keyfile": "relative.key"}):
            with self.subTest(bad=bad), self.assertRaises(ApiError):
                app.clean_job(self.job(**bad), self.cfg)

    def test_keyfile_password_kept_unless_cleared(self):
        existing = {"id": "j1", "keyfile_password": "pw"}
        self.assertEqual(app.clean_job(self.job(), self.cfg, existing)["keyfile_password"], "pw")
        cleared = app.clean_job(self.job(clear_keyfile_password=True), self.cfg, existing)
        self.assertEqual(cleared["keyfile_password"], "")
        self.assertNotIn("keyfile_password", app.public_job(cleared))


class ScheduleValidationTests(unittest.TestCase):
    def test_daily_needs_days(self):
        with self.assertRaises(ApiError):
            app.clean_schedule({"type": "daily", "time": "02:00", "days": []})

    def test_interval_must_divide_day(self):
        self.assertEqual(app.clean_schedule({"type": "hourly", "interval_hours": 6})["interval_hours"], 6)
        with self.assertRaises(ApiError):
            app.clean_schedule({"type": "hourly", "interval_hours": 5})

    def test_time_format(self):
        for bad in ("2:00", "24:00", "12:60", "noon"):
            with self.subTest(bad=bad), self.assertRaises(ApiError):
                app.clean_schedule({"type": "daily", "time": bad})

    def test_unknown_type(self):
        with self.assertRaises(ApiError):
            app.clean_schedule({"type": "monthly"})


class EmailValidationTests(unittest.TestCase):
    def test_disabled_allows_blank(self):
        e = app.clean_email({"enabled": False}, {})
        self.assertFalse(e["enabled"])

    def test_enabled_requires_fields(self):
        for missing in ("host", "from_addr", "to_addrs"):
            data = {"enabled": True, "host": "smtp.x.com", "from_addr": "a@x.com", "to_addrs": "b@x.com"}
            data[missing] = ""
            with self.subTest(missing=missing), self.assertRaises(ApiError):
                app.clean_email(data, {})

    def test_password_kept_and_hidden(self):
        e = app.clean_email({"enabled": False, "password": ""}, {"password": "old"})
        self.assertEqual(e["password"], "old")
        self.assertNotIn("password", app.public_email(e))
        self.assertTrue(app.public_email(e)["password_set"])

    def test_split_addrs(self):
        self.assertEqual(app.split_addrs("a@x.com, b@x.com;c@x.com d@x.com"),
                         ["a@x.com", "b@x.com", "c@x.com", "d@x.com"])


if __name__ == "__main__":
    unittest.main()
