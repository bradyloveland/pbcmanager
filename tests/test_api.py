"""End-to-end tests against the real HTTP server, scheduler-free.

Each test class starts the app on a random port with empty settings and a fake
proxmox-backup-client (tests/fake_client.py)."""
import json
import os
import tempfile
import time
import unittest

from tests.helpers import FINGERPRINT, PASSWORD, AppServer, Client, MiniSMTP, app, wait_for

TARGET = {"name": "Home PBS", "host": "192.0.2.10", "port": 8007, "datastore": "backup-pool",
          "username": "omv@pbs", "token_name": "omv", "secret": "token-secret", "fingerprint": FINGERPRINT}


class ServerCase(unittest.TestCase):
    server_settings = None
    email = None

    def setUp(self):
        self.server = AppServer(self.server_settings, self.email)
        self.client = Client(self.server)
        self.folders = tempfile.mkdtemp(prefix="pbsm-src-")

    def tearDown(self):
        self.server.close()

    def folder(self, name, size=100):
        path = os.path.join(self.folders, name)
        os.makedirs(path, exist_ok=True)
        with open(os.path.join(path, "file.bin"), "wb") as f:
            f.write(b"x" * size)
        return path

    def make_target(self, **over):
        status, data, _ = self.client.post("/api/targets", dict(TARGET, **over))
        self.assertEqual(status, 200, data)
        return data["target"]

    def make_job(self, target_id, paths, **over):
        body = {"name": "job", "target_id": target_id, "backup_id": "omv-test",
                "shares": [{"path": p} for p in paths], "schedule": {"type": "manual"}}
        body.update(over)
        status, data, _ = self.client.post("/api/jobs", body)
        self.assertEqual(status, 200, data)
        return data["job"]

    def run_job(self, job_id):
        status, data, _ = self.client.post(f"/api/jobs/{job_id}/run")
        self.assertEqual(status, 200, data)
        run_id = data["run"]["id"]

        def finished():
            _, d, _ = self.client.get(f"/api/runs/{run_id}")
            return d["run"] if d["run"]["status"] not in ("queued", "running") else None
        return wait_for(finished, timeout=20)


class AuthTests(ServerCase):
    def test_public_endpoints(self):
        self.assertEqual(self.client.get("/api/health")[1], {"ok": True})
        status, data, _ = self.client.get("/api/session")
        self.assertEqual(status, 200)
        self.assertIsNone(data["user"])
        self.assertFalse(data["setup_needed"])

    def test_everything_else_needs_sign_in(self):
        for path in ("/api/overview", "/api/targets", "/api/jobs", "/api/config/export", "/api/browse"):
            with self.subTest(path=path):
                self.assertEqual(self.client.get(path)[0], 401)

    def test_writes_need_csrf_header(self):
        self.client.login()
        status, _, _ = self.client.post("/api/targets", TARGET, csrf=False)
        self.assertEqual(status, 403)

    def test_login_logout(self):
        self.assertEqual(self.client.login()["user"], "admin")
        self.assertEqual(self.client.get("/api/overview")[0], 200)
        self.client.post("/api/logout")
        self.assertEqual(self.client.get("/api/overview")[0], 401)

    def test_wrong_password_then_throttled(self):
        for _ in range(5):
            status, _, _ = self.client.post("/api/login", {"username": "admin", "password": "nope"})
            self.assertEqual(status, 401)
        status, data, _ = self.client.post("/api/login", {"username": "admin", "password": PASSWORD})
        self.assertEqual(status, 429)
        self.assertIn("Try again", data["error"])

    def test_session_cookie_flags(self):
        _, _, headers = self.client.post("/api/login", {"username": "admin", "password": PASSWORD})
        cookie = headers.get("Set-Cookie", "")
        self.assertIn("HttpOnly", cookie)
        self.assertIn("SameSite=Strict", cookie)
        self.assertNotIn("Secure", cookie)  # plain HTTP, no trusted proxy

    def test_change_password_and_username(self):
        self.client.login()
        status, data, _ = self.client.post("/api/account/password", {"current": "wrong", "new": "x" * 12})
        self.assertEqual(status, 400)
        status, _, _ = self.client.post("/api/account/password", {"current": PASSWORD, "new": "new-password-123"})
        self.assertEqual(status, 200)
        status, data, _ = self.client.put("/api/account/username", {"username": "boss", "password": "new-password-123"})
        self.assertEqual((status, data["username"]), (200, "boss"))
        other = Client(self.server)
        status, _, _ = other.post("/api/login", {"username": "boss", "password": "new-password-123"})
        self.assertEqual(status, 200)


class TotpFlowTests(ServerCase):
    def enable(self):
        self.client.login()
        status, setup, _ = self.client.post("/api/account/totp/setup", {"password": PASSWORD})
        self.assertEqual(status, 200)
        self.assertIn("<svg", setup["qr_svg"])
        secret = setup["secret"].replace(" ", "")
        step = int(time.time() // 30)
        status, data, _ = self.client.post("/api/account/totp/enable", {"code": "000000"})
        self.assertEqual(status, 400)
        status, data, _ = self.client.post("/api/account/totp/enable", {"code": app.totp_code(secret, step)})
        self.assertEqual(status, 200, data)
        self.assertEqual(len(data["recovery_codes"]), 10)
        return secret, step, data["recovery_codes"]

    def test_setup_requires_password(self):
        self.client.login()
        self.assertEqual(self.client.post("/api/account/totp/setup", {"password": "nope"})[0], 400)

    def test_full_flow(self):
        secret, step, recovery = self.enable()
        fresh = Client(self.server)
        status, data, _ = fresh.post("/api/login", {"username": "admin", "password": PASSWORD})
        self.assertEqual(status, 200)
        self.assertTrue(data["totp_required"])
        self.assertEqual(fresh.get("/api/overview")[0], 401)  # password alone isn't enough
        ticket = data["ticket"]

        status, _, _ = fresh.post("/api/login/totp", {"ticket": ticket, "code": app.totp_code(secret, step)})
        self.assertEqual(status, 401, "replaying the enrollment code must fail")
        status, data, _ = fresh.post("/api/login/totp", {"ticket": ticket, "code": app.totp_code(secret, step + 1)})
        self.assertEqual(status, 200, data)
        self.assertFalse(data["recovery_used"])

        third = Client(self.server)
        ticket = third.post("/api/login", {"username": "admin", "password": PASSWORD})[1]["ticket"]
        status, data, _ = third.post("/api/login/totp", {"ticket": ticket, "code": recovery[0].upper()})
        self.assertEqual((status, data["recovery_used"], data["recovery_left"]), (200, True, 9))

        fourth = Client(self.server)
        ticket = fourth.post("/api/login", {"username": "admin", "password": PASSWORD})[1]["ticket"]
        self.assertEqual(fourth.post("/api/login/totp", {"ticket": ticket, "code": recovery[0]})[0], 401)

    def test_bad_ticket(self):
        self.enable()
        status, data, _ = Client(self.server).post("/api/login/totp", {"ticket": "nope", "code": "123456"})
        self.assertEqual(status, 401)
        self.assertIn("timed out", data["error"])

    def test_disable_needs_code(self):
        _, _, recovery = self.enable()
        status, _, _ = self.client.post("/api/account/totp/disable", {"password": PASSWORD, "code": "000000"})
        self.assertEqual(status, 400)
        status, data, _ = self.client.post("/api/account/totp/disable", {"password": PASSWORD, "code": recovery[1]})
        self.assertEqual((status, data["totp_enabled"]), (200, False))


class BackupFlowTests(ServerCase):
    def setUp(self):
        super().setUp()
        self.client.login()

    def test_test_connection(self):
        status, data, _ = self.client.post("/api/targets/test", TARGET)
        self.assertEqual((status, data["total"]), (200, 20 * 2**40))
        status, data, _ = self.client.post("/api/targets/test", dict(TARGET, secret="bad"))
        self.assertEqual(status, 502)
        self.assertIn("permission", data["error"])

    def test_secrets_never_returned(self):
        t = self.make_target()
        self.client.put("/api/email", {"enabled": False, "password": "mail-secret"})
        blob = json.dumps([self.client.get(p)[1] for p in ("/api/targets", "/api/email", "/api/config/export",
                                                           "/api/overview", "/api/jobs")])
        self.assertNotIn("token-secret", blob)
        self.assertNotIn("mail-secret", blob)
        self.assertTrue(t["secret_set"])

    def test_successful_backup(self):
        log_file = os.path.join(self.folders, "calls.jsonl")
        os.environ["FAKE_PBC_LOG"] = log_file
        self.addCleanup(os.environ.pop, "FAKE_PBC_LOG", None)
        t = self.make_target()
        job = self.make_job(t["id"], [self.folder("media")], excludes="/Movies\n/TV", rate="10MiB")
        run = self.run_job(job["id"])
        self.assertEqual(run["status"], "success", run)
        self.assertEqual(run["exit_code"], 0)

        _, logdata, _ = self.client.get(f"/api/runs/{run['id']}/log")
        self.assertIn("--exclude /Movies --exclude /TV", logdata["text"])
        self.assertTrue(logdata["done"])

        with open(log_file) as f:
            calls = [json.loads(line) for line in f]
        backup = next(c for c in calls if c["args"][0] == "backup")
        self.assertEqual(backup["env"]["PBS_REPOSITORY"], "omv@pbs!omv@192.0.2.10:8007:backup-pool")
        self.assertEqual(backup["env"]["PBS_PASSWORD"], "token-secret")
        self.assertIn("media.pxar:" + os.path.join(self.folders, "media"), backup["args"])

    def test_failed_backup_and_missing_folder(self):
        t = self.make_target()
        failing = self.make_job(t["id"], [self.folder("will-fail")], name="bad", backup_id="bad")
        run = self.run_job(failing["id"])
        self.assertEqual(run["status"], "failed")
        self.assertEqual(run["summary"], "Error: connection reset by peer")

        missing = self.make_job(t["id"], [os.path.join(self.folders, "not-mounted")], name="gone", backup_id="gone")
        run = self.run_job(missing["id"])
        self.assertEqual(run["status"], "failed")
        self.assertIn("aren't mounted", run["summary"])

    def test_one_run_per_job_and_cancel(self):
        t = self.make_target()
        job = self.make_job(t["id"], [self.folder("slow-folder")])
        _, first, _ = self.client.post(f"/api/jobs/{job['id']}/run")
        _, second, _ = self.client.post(f"/api/jobs/{job['id']}/run")
        self.assertTrue(first["created"])
        self.assertFalse(second["created"])
        self.assertEqual(first["run"]["id"], second["run"]["id"])
        run_id = first["run"]["id"]
        wait_for(lambda: self.client.get(f"/api/runs/{run_id}")[1]["run"]["status"] == "running")
        self.assertEqual(self.client.post(f"/api/runs/{run_id}/cancel")[0], 200)
        run = wait_for(lambda: (lambda r: r if r["status"] == "cancelled" else None)(
            self.client.get(f"/api/runs/{run_id}")[1]["run"]), timeout=25)
        self.assertEqual(run["status"], "cancelled")

    def test_missing_secret_blocks_run(self):
        t = self.make_target()
        job = self.make_job(t["id"], [self.folder("media")])
        with self.server.app.cfg.lock:
            self.server.app.cfg.data["targets"][0]["secret"] = ""
        run = self.run_job(job["id"])
        self.assertEqual(run["status"], "failed")
        self.assertIn("no token secret", run["summary"])

    def test_target_in_use_cannot_be_deleted(self):
        t = self.make_target()
        self.make_job(t["id"], [self.folder("media")])
        status, data, _ = self.client.delete(f"/api/targets/{t['id']}")
        self.assertEqual(status, 409)

    def test_dashboard_sizes(self):
        t = self.make_target()
        job = self.make_job(t["id"], [self.folder("media", 5000)])
        self.server.app.sizes.request(sources="*", jobs="*", targets="*")

        def ready():
            _, d, _ = self.client.get("/api/overview")
            z, dest = d["sizes"], d["destinations"][0]["usage"]
            return d if z["source_total"] and z["backup_total"] and dest else None
        d = wait_for(ready, timeout=20)
        self.assertGreaterEqual(d["sizes"]["source_total"], 5000)
        self.assertEqual(d["sizes"]["backup_total"], 1234567)  # newest snapshot from the fake client
        self.assertEqual(d["destinations"][0]["usage"]["avail"], 15 * 2**40)
        js = d["jobs"][0]["sizes"]
        self.assertEqual((js["snapshot_count"], js["backup_time"]), (2, 1790086400))
        self.assertEqual(self.client.post("/api/sizes/refresh", {"what": "sources"})[0], 200)
        self.assertEqual(self.client.post("/api/sizes/refresh", {"what": "bogus"})[0], 400)
        self.assertEqual(job["name"], "job")

    def test_browse(self):
        self.folder("alpha")
        self.folder("beta")
        status, data, _ = self.client.get("/api/browse?path=" + self.folders)
        self.assertEqual(status, 200)
        self.assertEqual(data["dirs"], ["alpha", "beta"])
        self.assertEqual(data["parent"], os.path.dirname(self.folders))


class EmailAlertTests(ServerCase):
    def setUp(self):
        self.smtp = MiniSMTP()
        self.email = {"enabled": True, "host": "127.0.0.1", "port": self.smtp.port, "security": "none",
                      "from_addr": "nas@example.com", "to_addrs": "me@example.com",
                      "notify_failure": True, "notify_success": False}
        super().setUp()
        self.client.login()

    def tearDown(self):
        super().tearDown()
        self.smtp.close()

    def test_test_email(self):
        status, _, _ = self.client.post("/api/email/test", self.email)
        self.assertEqual(status, 200)
        wait_for(lambda: self.smtp.messages)
        self.assertIn("Test email", self.smtp.messages[0])

    def test_failure_alert_contains_reason(self):
        t = self.make_target()
        job = self.make_job(t["id"], [self.folder("fail-here")], name="Nightly")
        self.run_job(job["id"])
        msg = wait_for(lambda: self.smtp.messages and self.smtp.messages[-1])
        self.assertIn("Subject: [PBS Manager] Backup failed: Nightly", msg)
        self.assertIn("connection reset by peer", msg)

    def test_no_alert_on_success_by_default(self):
        t = self.make_target()
        job = self.make_job(t["id"], [self.folder("ok")])
        self.run_job(job["id"])
        time.sleep(0.5)
        self.assertEqual(self.smtp.messages, [])


class ExportImportApiTests(ServerCase):
    def setUp(self):
        super().setUp()
        self.client.login()

    def test_export_then_import_preserves_local_secrets(self):
        t = self.make_target()
        self.make_job(t["id"], [self.folder("media")], name="media")
        _, exported, _ = self.client.get("/api/config/export")
        self.assertNotIn("token-secret", json.dumps(exported))

        exported["jobs"][0]["name"] = "media renamed"
        status, preview, _ = self.client.post("/api/config/import", {"config": exported})
        self.assertEqual((status, preview["applied"]), (200, False))
        self.assertEqual(self.client.get("/api/jobs")[1]["jobs"][0]["name"], "media")  # preview changes nothing

        status, done, _ = self.client.post("/api/config/import", {"config": exported, "apply": True})
        self.assertEqual((status, done["applied"], done["summary"]["needs_secret"]), (200, True, []))
        self.assertEqual(self.client.get("/api/jobs")[1]["jobs"][0]["name"], "media renamed")
        self.assertEqual(self.server.app.cfg.data["targets"][0]["secret"], "token-secret")

    def test_bad_file(self):
        status, data, _ = self.client.post("/api/config/import", {"config": {"hello": 1}})
        self.assertEqual(status, 400)
        self.assertIn("isn't a PBS Backup Manager", data["error"])


class ProxyTests(ServerCase):
    server_settings = {"trusted_proxies": ["127.0.0.1"], "base_path": "/backups"}

    def test_base_path(self):
        status, _, headers = self.client.get("/backups")
        self.assertEqual(status, 301)
        self.assertTrue(headers["Location"].endswith("/backups/"))
        prefixed = Client(self.server, prefix="/backups")
        self.assertEqual(prefixed.get("/api/health")[1], {"ok": True})
        self.assertEqual(self.client.get("/api/health")[1], {"ok": True})  # proxy that strips the prefix

    def test_secure_cookie_behind_https_proxy(self):
        _, _, headers = self.client.post("/api/login", {"username": "admin", "password": PASSWORD},
                                         headers={"X-Forwarded-Proto": "https"})
        self.assertIn("Secure", headers.get("Set-Cookie", ""))

    def test_throttle_uses_forwarded_client(self):
        for _ in range(5):
            self.client.post("/api/login", {"username": "admin", "password": "x"},
                             headers={"X-Forwarded-For": "198.51.100.7"})
        blocked = self.client.post("/api/login", {"username": "admin", "password": PASSWORD},
                                   headers={"X-Forwarded-For": "198.51.100.7"})
        other = self.client.post("/api/login", {"username": "admin", "password": PASSWORD},
                                 headers={"X-Forwarded-For": "198.51.100.8"})
        self.assertEqual((blocked[0], other[0]), (429, 200))


class UntrustedProxyTests(ServerCase):
    def test_forwarded_headers_ignored_from_untrusted_peer(self):
        for i in range(5):
            self.client.post("/api/login", {"username": "admin", "password": "x"},
                             headers={"X-Forwarded-For": f"203.0.113.{i}"})
        status, _, headers = self.client.post("/api/login", {"username": "admin", "password": PASSWORD},
                                              headers={"X-Forwarded-For": "203.0.113.99",
                                                       "X-Forwarded-Proto": "https"})
        self.assertEqual(status, 429, "spoofed X-Forwarded-For must not dodge throttling")


if __name__ == "__main__":
    unittest.main()
