"""Browser smoke test. Skipped unless Playwright and Chromium are installed:
    pip install playwright && python -m playwright install chromium
"""
import unittest

from tests.helpers import PASSWORD, AppServer, Client

try:
    from playwright.sync_api import sync_playwright
except ImportError:
    sync_playwright = None

TARGET = {"name": "Home PBS", "host": "10.0.0.5", "port": 8007, "datastore": "pool", "username": "omv@pbs",
          "token_name": "omv", "secret": "s", "fingerprint": ":".join(["ab"] * 32)}


@unittest.skipIf(sync_playwright is None, "playwright not installed")
class UiSmokeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = AppServer({"trusted_proxies": ["127.0.0.1"], "base_path": "/backups"})
        client = Client(cls.server)
        client.login()
        tid = client.post("/api/targets", TARGET)[1]["target"]["id"]
        client.post("/api/jobs", {"name": "media", "target_id": tid, "shares": [{"path": "/tmp"}],
                                  "schedule": {"type": "daily", "time": "02:00"}})
        try:
            cls.pw = sync_playwright().start()
            cls.browser = cls.pw.chromium.launch(args=["--no-proxy-server"])
        except Exception as exc:  # browser binaries missing
            raise unittest.SkipTest(f"Chromium unavailable: {exc}") from exc

    @classmethod
    def tearDownClass(cls):
        cls.browser.close()
        cls.pw.stop()
        cls.server.close()

    def test_sign_in_and_visit_every_page(self):
        page = self.browser.new_page()
        errors = []
        page.on("pageerror", lambda e: errors.append(str(e)))
        page.goto(self.server.url("/backups"))  # no trailing slash: server redirects
        page.fill("[name=username]", "admin")
        page.fill("#login-form [name=password]", PASSWORD)
        page.click("#login-form button[type=submit]")
        page.wait_for_selector(".jobrow")
        self.assertIn("Data protected", page.inner_text(".widgets"))
        self.assertIn("Destination space", page.inner_text(".widgets"))
        for route, marker in (("#/jobs", "Backup jobs"), ("#/destinations", "Destinations"),
                              ("#/activity", "Activity"), ("#/alerts", "Email alerts"),
                              ("#/account", "Export and import settings")):
            page.goto(self.server.url("/backups/" + route))
            page.wait_for_function("t => document.querySelector('main').innerText.includes(t)", arg=marker)
        self.assertEqual(errors, [])


if __name__ == "__main__":
    unittest.main()
