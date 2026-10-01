"""Shared fixtures for the test suite: temporary directories, an in-process
app server, an HTTP client with cookies and a tiny SMTP server."""
import http.cookiejar
import json
import os
import socketserver
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, ROOT)
FAKE_CLIENT = os.path.join(ROOT, "tests", "fake_client.py")
os.environ["PBSM_CLIENT"] = FAKE_CLIENT
_BASE = tempfile.mkdtemp(prefix="pbsm-test-")
os.environ.setdefault("PBSM_CONFIG_DIR", os.path.join(_BASE, "conf"))
os.environ.setdefault("PBSM_DATA_DIR", os.path.join(_BASE, "data"))

import app  # noqa: E402

# Expected failures (wrong passwords, failing backups) log warnings; keep test output clean.
app.log.addHandler(__import__("logging").NullHandler())
app.log.propagate = False

PASSWORD = "correct-horse-battery"
FINGERPRINT = ":".join(["ab"] * 32)


def fresh_dirs():
    """Point the app at new, empty settings and data directories."""
    root = tempfile.mkdtemp(prefix="pbsm-case-", dir=_BASE)
    app.configure_paths(os.path.join(root, "conf"), os.path.join(root, "data"))
    return root


class AppServer:
    """Runs the real app and HTTP server on a random local port."""

    def __init__(self, server_settings=None, email=None):
        self.root = fresh_dirs()
        cfg = app.Config(app.CONFIG_PATH)
        cfg.data["auth"]["password_hash"] = app.hash_password(PASSWORD, iterations=1000)
        cfg.data["server"].update(server_settings or {})
        if email:
            cfg.data["email"].update(email)
        cfg.save()
        app.SizeTracker.START_DELAY = 0
        app.APP = app.App()
        self.app = app.APP
        self.httpd = app.Server(("127.0.0.1", 0), app.Handler)
        self.port = self.httpd.server_address[1]
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    def url(self, path):
        return f"http://127.0.0.1:{self.port}{path}"

    def close(self):
        self.app.stop()
        self.httpd.shutdown()
        self.httpd.server_close()


class Client:
    """Minimal JSON HTTP client with a cookie jar and no proxy."""

    def __init__(self, server, prefix=""):
        self.server = server
        self.prefix = prefix
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(self.jar),
            _NoRedirect())

    def call(self, method, path, body=None, headers=None, csrf=True):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.server.url(self.prefix + path), data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if csrf:
            req.add_header("X-PBSM", "1")
        for k, v in (headers or {}).items():
            req.add_header(k, v)
        try:
            with self.opener.open(req, timeout=15) as resp:
                return resp.status, _decode(resp.read()), dict(resp.headers)
        except urllib.error.HTTPError as err:
            return err.code, _decode(err.read()), dict(err.headers)

    def get(self, path, **kw):
        return self.call("GET", path, **kw)

    def post(self, path, body=None, **kw):
        return self.call("POST", path, body if body is not None else {}, **kw)

    def put(self, path, body, **kw):
        return self.call("PUT", path, body, **kw)

    def delete(self, path, **kw):
        return self.call("DELETE", path, **kw)

    def login(self, password=PASSWORD):
        status, data, _ = self.post("/api/login", {"username": "admin", "password": password})
        assert status == 200, data
        return data


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def _decode(raw):
    try:
        return json.loads(raw) if raw else {}
    except ValueError:
        return {"_raw": raw.decode(errors="replace")}


def wait_for(predicate, timeout=15, interval=0.1):
    deadline = time.time() + timeout
    while time.time() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(interval)
    raise AssertionError("Timed out waiting for condition")


class MiniSMTP(socketserver.ThreadingTCPServer):
    """Accepts mail on localhost and stores each message's raw text."""
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self):
        super().__init__(("127.0.0.1", 0), _SMTPHandler)
        self.messages = []
        self.port = self.server_address[1]
        threading.Thread(target=self.serve_forever, daemon=True).start()

    def close(self):
        self.shutdown()
        self.server_close()


class _SMTPHandler(socketserver.StreamRequestHandler):
    def handle(self):
        def say(line):
            self.wfile.write((line + "\r\n").encode())
        say("220 test ESMTP")
        in_data, buf = False, []
        while True:
            raw = self.rfile.readline()
            if not raw:
                return
            line = raw.decode(errors="replace").rstrip("\r\n")
            if in_data:
                if line == ".":
                    self.server.messages.append("\n".join(buf))
                    in_data, buf = False, []
                    say("250 OK")
                else:
                    buf.append(line[1:] if line.startswith("..") else line)
                continue
            verb = line[:4].upper()
            if verb == "EHLO":
                say("250-test")
                say("250 SIZE 10000000")
            elif verb == "HELO":
                say("250 test")
            elif verb in ("MAIL", "RCPT", "RSET", "NOOP"):
                say("250 OK")
            elif verb == "DATA":
                in_data = True
                say("354 End data with <CR><LF>.<CR><LF>")
            elif verb == "QUIT":
                say("221 Bye")
                return
            else:
                say("502 Command not implemented")
