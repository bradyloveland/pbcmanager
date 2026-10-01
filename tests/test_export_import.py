"""Settings export (credentials removed) and import (credentials kept)."""
import copy
import json
import unittest

from tests.helpers import FINGERPRINT, app

ApiError = app.ApiError


def sample_config():
    data = copy.deepcopy(app.DEFAULT_CONFIG)
    data["auth"].update(username="admin", password_hash="pbkdf2_sha256$1$x$y",
                        totp_secret="ABCDEFGH", recovery_codes=["deadbeef"])
    data["email"].update(enabled=True, host="smtp.x.com", port=587, username="mailer",
                         password="mail-pass", from_addr="nas@x.com", to_addrs="me@x.com")
    data["targets"] = [{"id": "t1", "name": "Home", "host": "10.0.0.5", "port": 8007, "datastore": "pool",
                        "username": "omv@pbs", "token_name": "omv", "secret": "token-secret",
                        "fingerprint": FINGERPRINT}]
    data["jobs"] = [{"id": "j1", "name": "media", "target_id": "t1", "backup_id": "omv-media",
                     "shares": [{"path": "/srv/d/media", "archive": "media"}], "excludes": ["/TV"],
                     "schedule": {"type": "daily", "time": "02:00", "days": [0, 1, 2, 3, 4, 5, 6],
                                  "interval_hours": 1},
                     "change_detection_mode": "metadata", "rate": "", "keyfile": "/root/k.json",
                     "keyfile_password": "key-pass", "enabled": True}]
    data["settings"] = {"max_concurrent": 2, "keep_runs": 300}
    return data


SECRETS = ("token-secret", "mail-pass", "key-pass", "ABCDEFGH", "deadbeef", "pbkdf2_sha256")


class ExportTests(unittest.TestCase):
    def test_no_credentials_in_export(self):
        text = json.dumps(app.export_config(sample_config()))
        for secret in SECRETS:
            self.assertNotIn(secret, text)

    def test_export_keeps_everything_else(self):
        out = app.export_config(sample_config())
        self.assertEqual(out["format"], app.EXPORT_FORMAT)
        self.assertEqual(out["format_version"], app.EXPORT_VERSION)
        self.assertNotIn("auth", out)
        self.assertEqual(out["targets"][0]["fingerprint"], FINGERPRINT)
        self.assertEqual(out["targets"][0]["username"], "omv@pbs")
        self.assertEqual(out["jobs"][0]["excludes"], ["/TV"])
        self.assertEqual(out["jobs"][0]["keyfile"], "/root/k.json")
        self.assertEqual(out["email"]["username"], "mailer")
        self.assertEqual(out["settings"]["max_concurrent"], 2)
        self.assertIn("server", out)


class ImportTests(unittest.TestCase):
    def test_round_trip_onto_same_machine_keeps_secrets(self):
        current = sample_config()
        result, summary = app.build_import(current, app.export_config(current))
        self.assertEqual(result["targets"][0]["secret"], "token-secret")
        self.assertEqual(result["jobs"][0]["keyfile_password"], "key-pass")
        self.assertEqual(result["email"]["password"], "mail-pass")
        self.assertEqual(summary["needs_secret"], [])
        self.assertEqual(summary["removed_jobs"], [])
        self.assertEqual(result["settings"]["max_concurrent"], 2)

    def test_import_onto_new_machine_flags_missing_credentials(self):
        exported = app.export_config(sample_config())
        result, summary = app.build_import(copy.deepcopy(app.DEFAULT_CONFIG), exported)
        self.assertEqual(result["targets"][0]["secret"], "")
        self.assertEqual(summary["needs_secret"], ["Home"])
        self.assertEqual(summary["needs_keyfile_password"], ["media"])
        self.assertTrue(summary["email_password_needed"])
        self.assertEqual(result["jobs"][0]["target_id"], result["targets"][0]["id"])

    def test_reports_removed_items(self):
        current = sample_config()
        exported = app.export_config(current)
        exported["jobs"], exported["targets"] = [], []
        _, summary = app.build_import(current, exported)
        self.assertEqual(summary["removed_jobs"], ["media"])
        self.assertEqual(summary["removed_targets"], ["Home"])

    def test_rejects_wrong_or_newer_format(self):
        exported = app.export_config(sample_config())
        for bad in ({}, [], {"format": "other"}, dict(exported, format_version=99), dict(exported, format_version="x")):
            with self.subTest(bad=str(bad)[:40]), self.assertRaises(ApiError):
                app.build_import(sample_config(), bad)

    def test_rejects_job_with_unknown_destination(self):
        exported = app.export_config(sample_config())
        exported["jobs"][0]["target_id"] = "missing"
        with self.assertRaises(ApiError) as ctx:
            app.build_import(sample_config(), exported)
        self.assertIn("media", ctx.exception.message)

    def test_invalid_ids_are_replaced(self):
        exported = app.export_config(sample_config())
        exported["targets"][0]["id"] = "../../etc"
        exported["jobs"][0]["target_id"] = "../../etc"
        with self.assertRaises(ApiError):  # job now points at an id that was replaced
            app.build_import(sample_config(), exported)
        exported["jobs"] = []
        result, _ = app.build_import(sample_config(), exported)
        self.assertRegex(result["targets"][0]["id"], r"^\w+$")

    def test_validates_imported_values(self):
        exported = app.export_config(sample_config())
        exported["targets"][0]["host"] = "bad host"
        with self.assertRaises(ApiError):
            app.build_import(sample_config(), exported)
        exported = app.export_config(sample_config())
        exported["settings"]["max_concurrent"] = 500
        with self.assertRaises(ApiError):
            app.build_import(sample_config(), exported)


if __name__ == "__main__":
    unittest.main()
