#!/usr/bin/env python3
"""
PBS Backup Manager

A small web UI for running file-level backups with proxmox-backup-client:
manage Proxmox Backup Server destinations and their credentials, choose the
folders to back up, schedule jobs, watch logs, and get an email when a
backup fails.

Python 3.8+ standard library only. Runs as root because it has to read every
folder it backs up.
"""
import argparse
import base64
import copy
import datetime
import hashlib
import hmac
import ipaddress
import json
import logging
import os
import queue
import re
import secrets
import shlex
import shutil
import signal
import smtplib
import socket
import ssl
import struct
import subprocess
import sys
import threading
import time
import uuid
from email.message import EmailMessage
from email.utils import formatdate, make_msgid
from http import cookies
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

VERSION = "1.1.0"
APP_DIR = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, APP_DIR)
from qr import qr_svg  # noqa: E402

STATIC_DIR = os.path.join(APP_DIR, "static")
CONFIG_DIR = os.environ.get("PBSM_CONFIG_DIR", "/etc/pbs-manager")
DATA_DIR = os.environ.get("PBSM_DATA_DIR", "/var/lib/pbs-manager")
CONFIG_PATH = os.path.join(CONFIG_DIR, "config.json")
RUNS_PATH = os.path.join(DATA_DIR, "runs.json")
LOG_DIR = os.path.join(DATA_DIR, "logs")
SESSION_COOKIE = "pbsm_session"
SESSION_TTL = 12 * 3600
HOSTNAME = socket.gethostname()

log = logging.getLogger("pbs-manager")


def client_bin():
    return (os.environ.get("PBSM_CLIENT")
            or shutil.which("proxmox-backup-client")
            or "/usr/bin/proxmox-backup-client")


def now():
    return time.time()


def fmt_ts(ts):
    if not ts:
        return "-"
    return datetime.datetime.fromtimestamp(ts).strftime("%Y-%m-%d %H:%M:%S")


def fmt_dur(seconds):
    if seconds is None:
        return "-"
    seconds = int(seconds)
    h, rem = divmod(seconds, 3600)
    m, s = divmod(rem, 60)
    if h:
        return f"{h}h {m}m"
    if m:
        return f"{m}m {s}s"
    return f"{s}s"


def write_json_atomic(path, data, mode=0o600):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = f"{path}.tmp.{os.getpid()}.{threading.get_ident()}"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    with os.fdopen(fd, "w") as f:
        json.dump(data, f, indent=2)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)


# --------------------------------------------------------------------------
# Passwords
# --------------------------------------------------------------------------

def hash_password(password, iterations=310_000):
    salt = secrets.token_bytes(16)
    dk = hashlib.pbkdf2_hmac("sha256", password.encode(), salt, iterations)
    return "pbkdf2_sha256${}${}${}".format(
        iterations, base64.b64encode(salt).decode(), base64.b64encode(dk).decode())


def verify_password(password, stored):
    try:
        algo, iterations, salt, expected = stored.split("$")
        if algo != "pbkdf2_sha256":
            return False
        dk = hashlib.pbkdf2_hmac("sha256", password.encode(),
                                 base64.b64decode(salt), int(iterations))
        return hmac.compare_digest(dk, base64.b64decode(expected))
    except (ValueError, TypeError):
        return False


# --------------------------------------------------------------------------
# Two-step verification (TOTP, RFC 6238) and recovery codes
# --------------------------------------------------------------------------

TOTP_PERIOD = 30
TOTP_ISSUER = "PBS Manager"


def totp_new_secret():
    return base64.b32encode(secrets.token_bytes(20)).decode().rstrip("=")


def totp_code(secret, step, digits=6):
    key = base64.b32decode(secret.upper() + "=" * (-len(secret) % 8))
    digest = hmac.new(key, struct.pack(">Q", step), hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    value = struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7FFFFFFF
    return str(value % 10 ** digits).zfill(digits)


def totp_match(secret, code, last_step=0, window=1):
    """Return the matching time step, or None. Steps at or before last_step are
    rejected so a code can't be replayed."""
    code = re.sub(r"\s+", "", code or "")
    if not re.fullmatch(r"\d{6}", code) or not secret:
        return None
    current = int(time.time() // TOTP_PERIOD)
    for step in range(current - window, current + window + 1):
        if step > last_step and hmac.compare_digest(totp_code(secret, step), code):
            return step
    return None


def totp_uri(secret, username):
    from urllib.parse import quote
    label = quote(f"{TOTP_ISSUER} ({HOSTNAME}):{username}")
    return (f"otpauth://totp/{label}?secret={secret}&issuer={quote(TOTP_ISSUER)}"
            f"&algorithm=SHA1&digits=6&period={TOTP_PERIOD}")


RECOVERY_ALPHABET = "abcdefghjkmnpqrstuvwxyz23456789"


def new_recovery_codes(count=10):
    codes = []
    for _ in range(count):
        raw = "".join(secrets.choice(RECOVERY_ALPHABET) for _ in range(10))
        codes.append(f"{raw[:5]}-{raw[5:]}")
    return codes


def hash_recovery(code):
    norm = re.sub(r"[^a-z0-9]", "", (code or "").lower())
    return hashlib.sha256(norm.encode()).hexdigest()


# --------------------------------------------------------------------------
# Configuration
# --------------------------------------------------------------------------

DEFAULT_CONFIG = {
    "server": {"bind": "0.0.0.0", "port": 8099, "tls_cert": "", "tls_key": "",
               "trusted_proxies": [], "base_path": ""},
    "auth": {"username": "admin", "password_hash": "", "totp_secret": "",
             "totp_last_step": 0, "recovery_codes": []},
    "email": {
        "enabled": False, "host": "", "port": 587, "security": "starttls",
        "username": "", "password": "", "from_addr": "", "to_addrs": "",
        "notify_failure": True, "notify_success": False,
    },
    "settings": {"max_concurrent": 1, "keep_runs": 500},
    "targets": [],
    "jobs": [],
}


class Config:
    def __init__(self, path):
        self.path = path
        self.lock = threading.RLock()
        self.data = self._load()

    def _load(self):
        data = copy.deepcopy(DEFAULT_CONFIG)
        if os.path.exists(self.path):
            with open(self.path) as f:
                loaded = json.load(f)
            for key, value in loaded.items():
                if isinstance(value, dict) and isinstance(data.get(key), dict):
                    data[key].update(value)
                else:
                    data[key] = value
        return data

    def save(self):
        with self.lock:
            os.makedirs(os.path.dirname(self.path), mode=0o700, exist_ok=True)
            write_json_atomic(self.path, self.data)

    def snapshot(self):
        with self.lock:
            return copy.deepcopy(self.data)

    def find(self, kind, item_id):
        with self.lock:
            for item in self.data[kind]:
                if item["id"] == item_id:
                    return copy.deepcopy(item)
        return None


# --------------------------------------------------------------------------
# Validation
# --------------------------------------------------------------------------

class ApiError(Exception):
    def __init__(self, status, message):
        super().__init__(message)
        self.status = status
        self.message = message


NAME_RE = re.compile(r"^[^\x00-\x1f]{1,64}$")
DATASTORE_RE = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9._\-]*$")
USER_RE = re.compile(r"^[^\s@!:]+@[A-Za-z0-9._\-]+$")
TOKEN_RE = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9._\-]*$")
HOST_RE = re.compile(r"^(\[[0-9A-Fa-f:.]+\]|[0-9A-Fa-f]*:[0-9A-Fa-f:.]+|[A-Za-z0-9.\-]+)$")
FP_RE = re.compile(r"^([0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}$")
BACKUP_ID_RE = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9._\-]*$")
ARCHIVE_RE = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9_\-]*$")
RATE_RE = re.compile(r"^\d+(\.\d+)?\s*([KMGTP]i?)?B?$", re.I)
TIME_RE = re.compile(r"^([01]\d|2[0-3]):[0-5]\d$")
EMAIL_RE = re.compile(r"^[^@\s,;<>]+@[^@\s,;<>]+\.[^@\s,;<>]+$")


def get_str(data, key, default=""):
    value = data.get(key, default)
    if value is None:
        return ""
    if not isinstance(value, (str, int, float)):
        raise ApiError(400, f"{key} must be text.")
    return str(value).strip()


def require(cond, message):
    if not cond:
        raise ApiError(400, message)


def to_int(value, label, lo, hi, default=None):
    if value in (None, "") and default is not None:
        return default
    try:
        n = int(value)
    except (TypeError, ValueError):
        raise ApiError(400, f"{label} must be a whole number.")
    require(lo <= n <= hi, f"{label} must be between {lo} and {hi}.")
    return n


def clean_target(inp, existing=None):
    t = {"id": existing["id"] if existing else uuid.uuid4().hex[:12]}
    t["name"] = get_str(inp, "name")
    require(t["name"] and NAME_RE.match(t["name"]), "Give the destination a name (up to 64 characters).")
    t["host"] = get_str(inp, "host")
    require(t["host"] and HOST_RE.match(t["host"]), "Enter the PBS server's hostname or IP address.")
    t["port"] = to_int(inp.get("port"), "Port", 1, 65535, default=8007)
    t["datastore"] = get_str(inp, "datastore")
    require(DATASTORE_RE.match(t["datastore"]), "Enter the datastore name exactly as it appears in PBS.")
    t["username"] = get_str(inp, "username")
    require(USER_RE.match(t["username"]), "Enter the user with its realm, for example omv-backup@pbs.")
    t["token_name"] = get_str(inp, "token_name")
    require(not t["token_name"] or TOKEN_RE.match(t["token_name"]),
            "Token names can use letters, numbers, dots, dashes and underscores.")
    secret = inp.get("secret") or ""
    require(isinstance(secret, str), "Secret must be text.")
    t["secret"] = secret if secret else (existing or {}).get("secret", "")
    require(t["secret"], "Enter the API token secret (or the user's password if you aren't using a token).")
    fp = get_str(inp, "fingerprint").lower()
    require(not fp or FP_RE.match(fp),
            "The fingerprint should be 32 pairs of hex digits separated by colons.")
    t["fingerprint"] = fp
    return t


def repository(target):
    host = target["host"]
    if ":" in host and not host.startswith("["):
        host = f"[{host}]"
    auth = target["username"]
    if target.get("token_name"):
        auth += "!" + target["token_name"]
    return f"{auth}@{host}:{target['port']}:{target['datastore']}"


def public_target(t):
    out = {k: v for k, v in t.items() if k != "secret"}
    out["secret_set"] = bool(t.get("secret"))
    out["repository"] = repository(t)
    return out


def archive_from_path(path):
    base = os.path.basename(os.path.normpath(path)) or "root"
    name = re.sub(r"[^A-Za-z0-9_\-]+", "-", base).strip("-_") or "root"
    if not re.match(r"^[A-Za-z0-9_]", name):
        name = "a" + name
    return name[:48]


def clean_schedule(inp):
    inp = inp if isinstance(inp, dict) else {}
    s = {"type": get_str(inp, "type", "manual") or "manual"}
    require(s["type"] in ("manual", "hourly", "daily"), "Choose when the job should run.")
    s["time"] = get_str(inp, "time", "02:00") or "02:00"
    require(TIME_RE.match(s["time"]), "Enter the time as HH:MM (24-hour).")
    days = inp.get("days", list(range(7)))
    require(isinstance(days, list), "Days must be a list.")
    s["days"] = sorted({to_int(d, "Day", 0, 6) for d in days})
    if s["type"] == "daily":
        require(s["days"], "Pick at least one day of the week.")
    s["interval_hours"] = to_int(inp.get("interval_hours"), "Interval", 1, 24, default=1)
    require(24 % s["interval_hours"] == 0, "Choose an interval that divides the day evenly (1, 2, 3, 4, 6, 8, 12 or 24 hours).")
    return s


def clean_job(inp, cfg, existing=None):
    j = {"id": existing["id"] if existing else uuid.uuid4().hex[:12]}
    j["name"] = get_str(inp, "name")
    require(j["name"] and NAME_RE.match(j["name"]), "Give the job a name (up to 64 characters).")
    j["target_id"] = get_str(inp, "target_id")
    require(cfg.find("targets", j["target_id"]), "Choose where this job backs up to.")
    j["backup_id"] = get_str(inp, "backup_id") or HOSTNAME
    require(BACKUP_ID_RE.match(j["backup_id"]),
            "The backup ID can use letters, numbers, dots, dashes and underscores.")

    shares = inp.get("shares") or []
    require(isinstance(shares, list) and shares, "Add at least one folder to back up.")
    cleaned, seen = [], set()
    for share in shares:
        require(isinstance(share, dict), "Each folder must be an object.")
        path = get_str(share, "path")
        require(path.startswith("/"), f"Folder paths must be absolute: {path or '(blank)'}")
        path = os.path.normpath(path)
        archive = get_str(share, "archive") or archive_from_path(path)
        require(ARCHIVE_RE.match(archive),
                f"Archive name “{archive}” can only use letters, numbers, dashes and underscores.")
        require(archive not in seen, f"Two folders share the archive name “{archive}”. Give each a unique name.")
        seen.add(archive)
        cleaned.append({"path": path, "archive": archive})
    require(len(cleaned) <= 100, "A job can back up at most 100 folders.")
    j["shares"] = cleaned

    excludes = inp.get("excludes") or []
    if isinstance(excludes, str):
        excludes = excludes.splitlines()
    j["excludes"] = [e.strip() for e in excludes if isinstance(e, str) and e.strip()][:200]

    j["schedule"] = clean_schedule(inp.get("schedule"))
    mode = get_str(inp, "change_detection_mode", "metadata") or "metadata"
    require(mode in ("legacy", "data", "metadata"), "Unknown change detection mode.")
    j["change_detection_mode"] = mode
    j["rate"] = get_str(inp, "rate")
    require(not j["rate"] or RATE_RE.match(j["rate"]), "Enter the speed limit like 20MiB (per second).")
    j["keyfile"] = get_str(inp, "keyfile")
    require(not j["keyfile"] or j["keyfile"].startswith("/"), "The encryption key file path must be absolute.")
    kp = inp.get("keyfile_password") or ""
    require(isinstance(kp, str), "Key password must be text.")
    j["keyfile_password"] = kp if kp else (existing or {}).get("keyfile_password", "")
    if inp.get("clear_keyfile_password"):
        j["keyfile_password"] = ""
    j["enabled"] = bool(inp.get("enabled", True))
    return j


def public_job(j):
    out = {k: v for k, v in j.items() if k != "keyfile_password"}
    out["keyfile_password_set"] = bool(j.get("keyfile_password"))
    return out


def clean_email(inp, existing):
    e = {}
    e["enabled"] = bool(inp.get("enabled"))
    e["host"] = get_str(inp, "host")
    e["port"] = to_int(inp.get("port"), "SMTP port", 1, 65535, default=587)
    e["security"] = get_str(inp, "security", "starttls") or "starttls"
    require(e["security"] in ("starttls", "ssl", "none"), "Choose a connection security option.")
    e["username"] = get_str(inp, "username")
    pw = inp.get("password") or ""
    require(isinstance(pw, str), "Password must be text.")
    e["password"] = pw if pw else existing.get("password", "")
    if inp.get("clear_password"):
        e["password"] = ""
    e["from_addr"] = get_str(inp, "from_addr")
    e["to_addrs"] = get_str(inp, "to_addrs")
    e["notify_failure"] = bool(inp.get("notify_failure", True))
    e["notify_success"] = bool(inp.get("notify_success", False))
    if e["enabled"]:
        require(e["host"], "Enter your SMTP server to turn on email alerts.")
        require(EMAIL_RE.match(e["from_addr"]), "Enter a valid sender address.")
        recipients = split_addrs(e["to_addrs"])
        require(recipients, "Enter at least one recipient.")
        for r in recipients:
            require(EMAIL_RE.match(r), f"“{r}” isn't a valid email address.")
    return e


def split_addrs(value):
    return [a.strip() for a in re.split(r"[,;\s]+", value or "") if a.strip()]


def public_email(e):
    out = {k: v for k, v in e.items() if k != "password"}
    out["password_set"] = bool(e.get("password"))
    return out


# --------------------------------------------------------------------------
# Scheduling
# --------------------------------------------------------------------------

def next_run_time(job, after):
    """Next scheduled start strictly after `after` (epoch), or None."""
    if not job.get("enabled", True):
        return None
    sch = job.get("schedule") or {}
    kind = sch.get("type", "manual")
    if kind == "manual":
        return None
    hh, mm = (int(x) for x in sch.get("time", "02:00").split(":"))
    base = datetime.datetime.fromtimestamp(after).replace(second=0, microsecond=0)
    if kind == "hourly":
        step = max(1, int(sch.get("interval_hours", 1)))
        cand = base.replace(minute=mm)
        if cand.timestamp() <= after:
            cand += datetime.timedelta(hours=1)
        for _ in range(24 * 3):
            if cand.hour % step == 0:
                return cand.timestamp()
            cand += datetime.timedelta(hours=1)
        return None
    if kind == "daily":
        days = sch.get("days") or list(range(7))
        cand = base.replace(hour=hh, minute=mm)
        if cand.timestamp() <= after:
            cand += datetime.timedelta(days=1)
        for _ in range(8):
            if cand.weekday() in days:
                return cand.timestamp()
            cand += datetime.timedelta(days=1)
    return None


class Scheduler(threading.Thread):
    def __init__(self, app):
        super().__init__(daemon=True, name="scheduler")
        self.app = app
        self.lock = threading.Lock()
        self.next = {}

    def reset(self, job_id):
        with self.lock:
            self.next.pop(job_id, None)

    def next_for(self, job):
        with self.lock:
            if job["id"] in self.next:
                return self.next[job["id"]]
        return next_run_time(job, now())

    def run(self):
        while True:
            try:
                self.tick()
            except Exception:
                log.exception("Scheduler tick failed")
            time.sleep(15)

    def tick(self):
        t = now()
        jobs = self.app.cfg.snapshot()["jobs"]
        live = {j["id"] for j in jobs}
        due = []
        with self.lock:
            for job_id in list(self.next):
                if job_id not in live:
                    del self.next[job_id]
            for job in jobs:
                if job["id"] not in self.next:
                    self.next[job["id"]] = next_run_time(job, t)
                    continue
                nxt = self.next[job["id"]]
                if nxt and t >= nxt:
                    due.append(job["id"])
                    self.next[job["id"]] = next_run_time(job, t + 1)
        for job_id in due:
            run, created = self.app.runner.enqueue(job_id, "schedule")
            if not created:
                log.info("Skipped scheduled run of %s: previous run still active", job_id)


# --------------------------------------------------------------------------
# Run history
# --------------------------------------------------------------------------

class RunStore:
    def __init__(self, path, keep):
        self.path = path
        self.keep = keep
        self.lock = threading.RLock()
        self.runs = []
        if os.path.exists(path):
            try:
                with open(path) as f:
                    self.runs = json.load(f)
            except (OSError, ValueError):
                log.exception("Could not read run history; starting fresh")

    def _save(self):
        if len(self.runs) > self.keep:
            for old in self.runs[self.keep:]:
                try:
                    os.remove(os.path.join(LOG_DIR, f"{old['id']}.log"))
                except OSError:
                    pass
            self.runs = self.runs[:self.keep]
        write_json_atomic(self.path, self.runs)

    def add(self, run):
        with self.lock:
            self.runs.insert(0, run)
            self._save()

    def update(self, run_id, **fields):
        with self.lock:
            for r in self.runs:
                if r["id"] == run_id:
                    r.update(fields)
                    self._save()
                    return dict(r)
        return None

    def get(self, run_id):
        with self.lock:
            for r in self.runs:
                if r["id"] == run_id:
                    return dict(r)
        return None

    def list(self, job_id=None, limit=100, statuses=None):
        with self.lock:
            out = []
            for r in self.runs:
                if job_id and r["job_id"] != job_id:
                    continue
                if statuses and r["status"] not in statuses:
                    continue
                out.append(dict(r))
                if len(out) >= limit:
                    break
            return out


# --------------------------------------------------------------------------
# Running backups
# --------------------------------------------------------------------------

class RunError(Exception):
    pass


def build_backup_cmd(job):
    cmd = [client_bin(), "backup"]
    for share in job["shares"]:
        cmd.append(f"{share['archive']}.pxar:{share['path']}")
    cmd += ["--backup-id", job["backup_id"]]
    if job.get("change_detection_mode") in ("data", "metadata"):
        cmd += ["--change-detection-mode", job["change_detection_mode"]]
    if job.get("rate"):
        cmd += ["--rate", job["rate"].replace(" ", "")]
    for pattern in job.get("excludes", []):
        cmd += ["--exclude", pattern]
    if job.get("keyfile"):
        cmd += ["--keyfile", job["keyfile"]]
    return cmd


def client_env(target, job=None):
    env = dict(os.environ)
    env.setdefault("HOME", "/root")
    env["PBS_REPOSITORY"] = repository(target)
    env["PBS_PASSWORD"] = target["secret"]
    if target.get("fingerprint"):
        env["PBS_FINGERPRINT"] = target["fingerprint"]
    else:
        env.pop("PBS_FINGERPRINT", None)
    if job and job.get("keyfile_password"):
        env["PBS_ENCRYPTION_PASSWORD"] = job["keyfile_password"]
    return env


def tail_file(path, max_bytes=6000):
    try:
        with open(path, "rb") as f:
            f.seek(0, os.SEEK_END)
            size = f.tell()
            f.seek(max(0, size - max_bytes))
            return f.read().decode("utf-8", "replace")
    except OSError:
        return ""


def summarize_error(text):
    lines = [ln.strip() for ln in text.splitlines() if ln.strip()]
    for ln in reversed(lines):
        if re.search(r"\berror\b", ln, re.I):
            return ln[:300]
    return (lines[-1] if lines else "The backup client exited with an error.")[:300]


class Runner:
    def __init__(self, app, workers):
        self.app = app
        self.q = queue.Queue()
        self.lock = threading.Lock()
        self.procs = {}
        for i in range(max(1, workers)):
            threading.Thread(target=self._worker, daemon=True, name=f"worker-{i}").start()

    def enqueue(self, job_id, trigger):
        job = self.app.cfg.find("jobs", job_id)
        if not job:
            raise ApiError(404, "That job no longer exists.")
        with self.lock:
            active = self.app.runs.list(job_id, limit=5, statuses=("queued", "running"))
            if active:
                return active[0], False
            target = self.app.cfg.find("targets", job["target_id"])
            run = {
                "id": uuid.uuid4().hex[:16], "job_id": job_id, "job_name": job["name"],
                "target_name": target["name"] if target else "", "trigger": trigger,
                "status": "queued", "queued_at": now(), "started": None, "ended": None,
                "exit_code": None, "summary": "", "email_error": "",
            }
            self.app.runs.add(run)
        self.q.put(run["id"])
        return run, True

    def cancel(self, run_id):
        run = self.app.runs.get(run_id)
        if not run:
            raise ApiError(404, "That run doesn't exist.")
        if run["status"] == "queued":
            self.app.runs.update(run_id, status="cancelled", ended=now(),
                                 summary="Cancelled before it started.")
            return
        if run["status"] != "running":
            raise ApiError(409, "This run has already finished.")
        self.app.runs.update(run_id, cancel_requested=True)
        with self.lock:
            proc = self.procs.get(run_id)
        if proc:
            try:
                os.killpg(proc.pid, signal.SIGINT)
            except ProcessLookupError:
                return

            def force_kill():
                time.sleep(20)
                if proc.poll() is None:
                    try:
                        os.killpg(proc.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
            threading.Thread(target=force_kill, daemon=True).start()

    def _worker(self):
        while True:
            run_id = self.q.get()
            try:
                run = self.app.runs.get(run_id)
                if run and run["status"] == "queued":
                    self._execute(run)
            except Exception:
                log.exception("Worker failed on run %s", run_id)

    def _execute(self, run):
        run_id = run["id"]
        job = self.app.cfg.find("jobs", run["job_id"])
        target = self.app.cfg.find("targets", job["target_id"]) if job else None
        started = now()
        self.app.runs.update(run_id, status="running", started=started)
        os.makedirs(LOG_DIR, mode=0o700, exist_ok=True)
        log_path = os.path.join(LOG_DIR, f"{run_id}.log")
        status, summary, rc = "failed", "", None

        with open(log_path, "a", buffering=1) as lf:
            def note(message):
                lf.write(f"[{fmt_ts(now())}] {message}\n")
                lf.flush()
            try:
                if not job:
                    raise RunError("The job was deleted before it could run.")
                if not target:
                    raise RunError("This job's destination no longer exists. Edit the job and choose one.")
                binary = build_backup_cmd(job)[0]
                if not os.path.exists(binary):
                    raise RunError("proxmox-backup-client isn't installed on this machine.")
                missing = [s["path"] for s in job["shares"] if not os.path.isdir(s["path"])]
                if missing:
                    raise RunError("These folders don't exist or aren't mounted: " + ", ".join(missing))
                if job.get("keyfile") and not os.path.isfile(job["keyfile"]):
                    raise RunError(f"The encryption key file {job['keyfile']} doesn't exist.")

                cmd = build_backup_cmd(job)
                note(f"Backing up {len(job['shares'])} folder(s) to {target['name']} ({repository(target)})")
                note("Command: " + shlex.join(cmd))
                proc = subprocess.Popen(cmd, stdout=lf, stderr=subprocess.STDOUT,
                                        stdin=subprocess.DEVNULL, env=client_env(target, job),
                                        start_new_session=True)
                with self.lock:
                    self.procs[run_id] = proc
                rc = proc.wait()
                with self.lock:
                    self.procs.pop(run_id, None)

                if (self.app.runs.get(run_id) or {}).get("cancel_requested"):
                    status, summary = "cancelled", "Cancelled while running."
                elif rc == 0:
                    status, summary = "success", "Backup finished."
                else:
                    status = "failed"
                    summary = summarize_error(tail_file(log_path))
                note(f"Finished with exit code {rc}.")
            except RunError as exc:
                summary = str(exc)
                note(summary)
            except Exception as exc:
                log.exception("Run %s crashed", run_id)
                summary = f"Unexpected error: {exc}"
                note(summary)

        ended = now()
        run = self.app.runs.update(run_id, status=status, ended=ended, exit_code=rc, summary=summary)
        log.info("Run %s (%s) %s", run_id, run and run["job_name"], status)
        if run:
            self.app.notifier.run_finished(run)


# --------------------------------------------------------------------------
# Email alerts
# --------------------------------------------------------------------------

class Notifier:
    def __init__(self, app):
        self.app = app

    def send(self, subject, body, settings=None):
        e = settings or self.app.cfg.snapshot()["email"]
        recipients = split_addrs(e.get("to_addrs"))
        if not (e.get("host") and e.get("from_addr") and recipients):
            raise RuntimeError("Email alerts need an SMTP server, a sender and at least one recipient.")
        msg = EmailMessage()
        msg["Subject"] = subject
        msg["From"] = e["from_addr"]
        msg["To"] = ", ".join(recipients)
        msg["Date"] = formatdate(localtime=True)
        msg["Message-ID"] = make_msgid(domain=HOSTNAME if "." in HOSTNAME else None)
        msg.set_content(body)
        context = ssl.create_default_context()
        port = int(e.get("port") or 587)
        if e.get("security") == "ssl":
            server = smtplib.SMTP_SSL(e["host"], port, timeout=30, context=context)
        else:
            server = smtplib.SMTP(e["host"], port, timeout=30)
        try:
            server.ehlo()
            if e.get("security") == "starttls":
                server.starttls(context=context)
                server.ehlo()
            if e.get("username"):
                server.login(e["username"], e.get("password", ""))
            server.send_message(msg)
        finally:
            try:
                server.quit()
            except Exception:
                pass

    def run_finished(self, run):
        e = self.app.cfg.snapshot()["email"]
        if not e.get("enabled"):
            return
        failed = run["status"] == "failed"
        if failed and not e.get("notify_failure"):
            return
        if run["status"] == "success" and not e.get("notify_success"):
            return
        if run["status"] not in ("failed", "success"):
            return
        threading.Thread(target=self._send_run, args=(run,), daemon=True).start()

    def _send_run(self, run):
        verb = "failed" if run["status"] == "failed" else "succeeded"
        duration = (run["ended"] - run["started"]) if run.get("started") and run.get("ended") else None
        job = self.app.cfg.find("jobs", run["job_id"])
        target = self.app.cfg.find("targets", job["target_id"]) if job else None
        lines = [
            f"Backup job \"{run['job_name']}\" {verb} on {HOSTNAME}.",
            "",
            f"Destination: {run.get('target_name') or '-'}" + (f" ({repository(target)})" if target else ""),
            f"Started:     {fmt_ts(run.get('started'))}",
            f"Finished:    {fmt_ts(run.get('ended'))} ({fmt_dur(duration)})",
            f"Exit code:   {run.get('exit_code') if run.get('exit_code') is not None else '-'}",
            f"Result:      {run.get('summary') or '-'}",
        ]
        if job:
            lines.append("Folders:     " + ", ".join(s["path"] for s in job["shares"]))
        if run["status"] == "failed":
            tail = tail_file(os.path.join(LOG_DIR, f"{run['id']}.log"), 4000).splitlines()[-40:]
            lines += ["", "Last lines of the log:", "-" * 60] + tail
        subject = f"[PBS Manager] Backup {verb}: {run['job_name']} on {HOSTNAME}"
        try:
            self.send(subject, "\n".join(lines) + "\n")
            self.app.runs.update(run["id"], email_error="")
        except Exception as exc:
            log.error("Could not send alert for run %s: %s", run["id"], exc)
            self.app.runs.update(run["id"], email_error=str(exc)[:300])


# --------------------------------------------------------------------------
# Application state
# --------------------------------------------------------------------------

class App:
    def __init__(self):
        os.makedirs(DATA_DIR, mode=0o700, exist_ok=True)
        os.makedirs(LOG_DIR, mode=0o700, exist_ok=True)
        self.cfg = Config(CONFIG_PATH)
        settings = self.cfg.data["settings"]
        self.runs = RunStore(RUNS_PATH, int(settings.get("keep_runs", 500)))
        self.notifier = Notifier(self)
        self.sessions = {}
        self.session_lock = threading.Lock()
        self.failures = {}
        self.account_failures = [0, 0.0]
        self.pending_logins = {}
        self.totp_pending = {}
        self.trusted_nets = parse_networks(self.cfg.data["server"].get("trusted_proxies", []))
        self.base_path = normalize_base_path(self.cfg.data["server"].get("base_path", ""))
        self.client_version = detect_client_version()
        self._recover_interrupted()
        self.runner = Runner(self, int(settings.get("max_concurrent", 1)))
        self.scheduler = Scheduler(self)
        self.scheduler.start()

    def _recover_interrupted(self):
        for run in self.runs.list(limit=10_000, statuses=("queued", "running")):
            if run["status"] == "queued":
                self.runs.update(run["id"], status="cancelled", ended=now(),
                                 summary="Dropped from the queue when the manager restarted.")
            else:
                updated = self.runs.update(
                    run["id"], status="failed", ended=now(),
                    summary="Interrupted: the manager stopped while this backup was running.")
                if updated:
                    self.notifier.run_finished(updated)

    # sessions
    def create_session(self, username):
        token = secrets.token_urlsafe(32)
        with self.session_lock:
            self.sessions[token] = {"user": username, "expires": now() + SESSION_TTL}
        return token

    def session_user(self, token):
        if not token:
            return None
        with self.session_lock:
            s = self.sessions.get(token)
            if not s:
                return None
            if s["expires"] < now():
                del self.sessions[token]
                return None
            s["expires"] = now() + SESSION_TTL
            return s["user"]

    def drop_session(self, token):
        with self.session_lock:
            self.sessions.pop(token, None)

    def drop_all_sessions(self, keep=None):
        with self.session_lock:
            for token in list(self.sessions):
                if token != keep:
                    del self.sessions[token]

    def is_trusted(self, addr):
        ip = parse_ip(addr)
        return bool(ip) and any(ip in net for net in self.trusted_nets)

    # brute-force protection, by client IP and for the account as a whole
    def check_throttle(self, ip):
        count, until = self.failures.get(ip, (0, 0))
        wait = max(until, self.account_failures[1]) - now()
        if wait > 0:
            raise ApiError(429, f"Too many attempts. Try again in {int(wait) + 1} seconds.")

    def record_failure(self, ip):
        count, _ = self.failures.get(ip, (0, 0))
        count += 1
        self.failures[ip] = (count, now() + 60 if count % 5 == 0 else 0)
        self.account_failures[0] += 1
        if self.account_failures[0] % 20 == 0:
            self.account_failures[1] = now() + 300
            log.warning("Sign-in paused for 5 minutes after %d failed attempts", self.account_failures[0])
        time.sleep(1)

    def clear_failures(self, ip):
        self.failures.pop(ip, None)
        self.account_failures = [0, 0.0]


def parse_ip(addr):
    try:
        ip = ipaddress.ip_address((addr or "").strip().strip("[]"))
    except ValueError:
        return None
    if ip.version == 6 and ip.ipv4_mapped:
        return ip.ipv4_mapped
    return ip


def parse_networks(items):
    nets = []
    for item in items or []:
        try:
            nets.append(ipaddress.ip_network(str(item).strip(), strict=False))
        except ValueError:
            log.warning("Ignoring invalid trusted proxy entry: %s", item)
    return nets


def normalize_base_path(path):
    path = (path or "").strip()
    if not path or path == "/":
        return ""
    return "/" + path.strip("/")


def detect_client_version():
    binary = client_bin()
    if not os.path.exists(binary):
        return None
    try:
        out = subprocess.run([binary, "version"], capture_output=True, text=True, timeout=15)
        text = (out.stdout or out.stderr).strip()
        m = re.search(r"(\d+\.\d+(\.\d+)?)", text)
        return m.group(1) if m else (text or "installed")
    except Exception:
        return "installed"


def run_client(target, args, timeout):
    binary = client_bin()
    if not os.path.exists(binary):
        raise ApiError(400, "proxmox-backup-client isn't installed on this machine.")
    try:
        out = subprocess.run([binary] + args, capture_output=True, text=True, timeout=timeout,
                             stdin=subprocess.DEVNULL, env=client_env(target))
    except subprocess.TimeoutExpired:
        raise ApiError(504, f"The PBS server didn't answer within {timeout} seconds.")
    if out.returncode != 0:
        raise ApiError(502, summarize_error((out.stderr or "") + "\n" + (out.stdout or "")))
    return out.stdout


# --------------------------------------------------------------------------
# API handlers
# --------------------------------------------------------------------------

APP = None
ROUTES = []


def route(method, pattern, auth=True):
    def deco(fn):
        ROUTES.append((method, re.compile(pattern), fn, auth))
        return fn
    return deco


@route("GET", r"/api/health", auth=False)
def api_health(h, body, qs):
    return {"ok": True}


@route("GET", r"/api/session", auth=False)
def api_session(h, body, qs):
    user = APP.session_user(h.session_token())
    return {"user": user, "host": HOSTNAME, "version": VERSION,
            "setup_needed": not APP.cfg.data["auth"].get("password_hash")}


@route("POST", r"/api/login", auth=False)
def api_login(h, body, qs):
    ip = h.client_ip()
    APP.check_throttle(ip)
    auth = APP.cfg.snapshot()["auth"]
    if not auth.get("password_hash"):
        raise ApiError(403, "No admin password is set. Run: sudo pbs-manager passwd")
    username = get_str(body, "username")
    password = body.get("password") or ""
    if not (hmac.compare_digest(username.lower(), auth["username"].lower())
            and verify_password(password, auth["password_hash"])):
        APP.record_failure(ip)
        log.warning("Failed sign-in for %r from %s", username, ip)
        raise ApiError(401, "That username and password don't match.")
    if auth.get("totp_secret"):
        ticket = secrets.token_urlsafe(24)
        for t, p in list(APP.pending_logins.items()):
            if p["expires"] < now():
                APP.pending_logins.pop(t, None)
        APP.pending_logins[ticket] = {"user": auth["username"], "expires": now() + 300, "attempts": 0}
        return {"totp_required": True, "ticket": ticket}
    APP.clear_failures(ip)
    h.set_cookie = APP.create_session(auth["username"])
    log.info("Signed in: %s from %s", auth["username"], ip)
    return {"user": auth["username"]}


def consume_second_factor(code):
    """Check a TOTP or recovery code against the saved secret. Returns 'totp',
    'recovery' or None, and records use so codes can't be reused."""
    with APP.cfg.lock:
        auth = APP.cfg.data["auth"]
        step = totp_match(auth.get("totp_secret"), code, auth.get("totp_last_step", 0))
        if step:
            auth["totp_last_step"] = step
            APP.cfg.save()
            return "totp"
        digest = hash_recovery(code)
        if len(re.sub(r"[^a-z0-9]", "", (code or "").lower())) == 10:
            for stored in auth.get("recovery_codes", []):
                if hmac.compare_digest(stored, digest):
                    auth["recovery_codes"] = [c for c in auth["recovery_codes"] if c != stored]
                    APP.cfg.save()
                    return "recovery"
    return None


@route("POST", r"/api/login/totp", auth=False)
def api_login_totp(h, body, qs):
    ip = h.client_ip()
    APP.check_throttle(ip)
    ticket = get_str(body, "ticket")
    pending = APP.pending_logins.get(ticket)
    if not pending or pending["expires"] < now():
        APP.pending_logins.pop(ticket, None)
        raise ApiError(401, "Your sign-in timed out. Enter your password again.")
    kind = consume_second_factor(get_str(body, "code"))
    if not kind:
        pending["attempts"] += 1
        if pending["attempts"] >= 5:
            APP.pending_logins.pop(ticket, None)
        APP.record_failure(ip)
        log.warning("Failed verification code for %s from %s", pending["user"], ip)
        raise ApiError(401, "That code isn't valid. Codes change every 30 seconds, so check your device's clock if this keeps happening.")
    APP.pending_logins.pop(ticket, None)
    APP.clear_failures(ip)
    h.set_cookie = APP.create_session(pending["user"])
    log.info("Signed in with %s code: %s from %s", kind, pending["user"], ip)
    left = len(APP.cfg.snapshot()["auth"].get("recovery_codes", []))
    return {"user": pending["user"], "recovery_used": kind == "recovery", "recovery_left": left}


@route("POST", r"/api/logout", auth=False)
def api_logout(h, body, qs):
    APP.drop_session(h.session_token())
    h.set_cookie = ""
    return {"ok": True}


@route("POST", r"/api/account/password")
def api_password(h, body, qs):
    auth = APP.cfg.snapshot()["auth"]
    require(verify_password(body.get("current") or "", auth["password_hash"]), "Your current password is incorrect.")
    new = body.get("new") or ""
    require(isinstance(new, str) and len(new) >= 10, "Use at least 10 characters for the new password.")
    with APP.cfg.lock:
        APP.cfg.data["auth"]["password_hash"] = hash_password(new)
        APP.cfg.save()
    APP.drop_all_sessions(keep=h.session_token())
    return {"ok": True}


def require_password(body, field="password"):
    auth = APP.cfg.snapshot()["auth"]
    if not verify_password(body.get(field) or "", auth["password_hash"]):
        raise ApiError(400, "Your current password is incorrect.")
    return auth


def account_info():
    auth = APP.cfg.snapshot()["auth"]
    return {"username": auth["username"], "totp_enabled": bool(auth.get("totp_secret")),
            "recovery_left": len(auth.get("recovery_codes", []))}


@route("GET", r"/api/account")
def api_account(h, body, qs):
    return account_info()


@route("PUT", r"/api/account/username")
def api_account_username(h, body, qs):
    require_password(body)
    username = get_str(body, "username")
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._@\-]{1,63}", username or ""),
            "Usernames are 2-64 characters: letters, numbers, dots, dashes, underscores or @.")
    with APP.cfg.lock:
        APP.cfg.data["auth"]["username"] = username
        APP.cfg.save()
    with APP.session_lock:
        for sess in APP.sessions.values():
            sess["user"] = username
    return account_info()


@route("POST", r"/api/account/totp/setup")
def api_totp_setup(h, body, qs):
    auth = require_password(body)
    secret = totp_new_secret()
    APP.totp_pending[h.session_token()] = {"secret": secret, "expires": now() + 600}
    uri = totp_uri(secret, auth["username"])
    grouped = " ".join(secret[i:i + 4] for i in range(0, len(secret), 4))
    return {"secret": grouped, "uri": uri, "qr_svg": qr_svg(uri)}


@route("POST", r"/api/account/totp/enable")
def api_totp_enable(h, body, qs):
    pending = APP.totp_pending.get(h.session_token())
    if not pending or pending["expires"] < now():
        raise ApiError(400, "Setup timed out. Start again to get a new QR code.")
    step = totp_match(pending["secret"], get_str(body, "code"))
    if not step:
        raise ApiError(400, "That code doesn't match. Make sure you scanned the code shown here and your phone's clock is set automatically.")
    codes = new_recovery_codes()
    with APP.cfg.lock:
        auth = APP.cfg.data["auth"]
        auth["totp_secret"] = pending["secret"]
        auth["totp_last_step"] = step
        auth["recovery_codes"] = [hash_recovery(c) for c in codes]
        APP.cfg.save()
    APP.totp_pending.pop(h.session_token(), None)
    APP.drop_all_sessions(keep=h.session_token())
    log.info("Two-step verification turned on")
    return {"recovery_codes": codes, **account_info()}


@route("POST", r"/api/account/totp/disable")
def api_totp_disable(h, body, qs):
    require_password(body)
    if not consume_second_factor(get_str(body, "code")):
        raise ApiError(400, "Enter a current code from your authenticator app, or a recovery code.")
    with APP.cfg.lock:
        auth = APP.cfg.data["auth"]
        auth.update(totp_secret="", totp_last_step=0, recovery_codes=[])
        APP.cfg.save()
    log.info("Two-step verification turned off")
    return account_info()


@route("POST", r"/api/account/totp/recovery")
def api_totp_recovery(h, body, qs):
    require_password(body)
    if not consume_second_factor(get_str(body, "code")):
        raise ApiError(400, "Enter a current code from your authenticator app, or a recovery code.")
    codes = new_recovery_codes()
    with APP.cfg.lock:
        APP.cfg.data["auth"]["recovery_codes"] = [hash_recovery(c) for c in codes]
        APP.cfg.save()
    return {"recovery_codes": codes, **account_info()}


@route("GET", r"/api/overview")
def api_overview(h, body, qs):
    cfg = APP.cfg.snapshot()
    targets = {t["id"]: t for t in cfg["targets"]}
    jobs = []
    for job in cfg["jobs"]:
        recent = APP.runs.list(job["id"], limit=20)
        last_ok = next((r for r in APP.runs.list(job["id"], limit=500, statuses=("success",))), None)
        finished = next((r for r in recent if r["status"] in ("success", "failed")), None)
        jobs.append({
            "id": job["id"], "name": job["name"], "enabled": job.get("enabled", True),
            "target_name": targets.get(job["target_id"], {}).get("name", ""),
            "shares": len(job["shares"]), "schedule": job["schedule"],
            "recent": [{k: r.get(k) for k in ("id", "status", "started", "ended", "queued_at", "summary")}
                       for r in recent],
            "last_finished": finished and {k: finished.get(k) for k in ("id", "status", "ended", "summary")},
            "last_success_at": last_ok and last_ok.get("ended"),
            "next_run": APP.scheduler.next_for(job),
        })
    return {
        "jobs": jobs, "target_count": len(cfg["targets"]), "host": HOSTNAME,
        "client_version": APP.client_version, "email_enabled": cfg["email"].get("enabled", False),
        "now": now(),
    }


@route("GET", r"/api/targets")
def api_targets(h, body, qs):
    return {"targets": [public_target(t) for t in APP.cfg.snapshot()["targets"]]}


@route("POST", r"/api/targets")
def api_target_create(h, body, qs):
    t = clean_target(body)
    with APP.cfg.lock:
        APP.cfg.data["targets"].append(t)
        APP.cfg.save()
    return {"target": public_target(t)}


@route("PUT", r"/api/targets/(\w+)")
def api_target_update(h, body, qs, tid):
    existing = APP.cfg.find("targets", tid)
    if not existing:
        raise ApiError(404, "That destination doesn't exist.")
    t = clean_target(body, existing)
    with APP.cfg.lock:
        APP.cfg.data["targets"] = [t if x["id"] == tid else x for x in APP.cfg.data["targets"]]
        APP.cfg.save()
    return {"target": public_target(t)}


@route("DELETE", r"/api/targets/(\w+)")
def api_target_delete(h, body, qs, tid):
    with APP.cfg.lock:
        users = [j["name"] for j in APP.cfg.data["jobs"] if j["target_id"] == tid]
        if users:
            raise ApiError(409, "Still used by: " + ", ".join(users) + ". Move those jobs to another destination first.")
        APP.cfg.data["targets"] = [x for x in APP.cfg.data["targets"] if x["id"] != tid]
        APP.cfg.save()
    return {"ok": True}


@route("POST", r"/api/targets/test")
def api_target_test(h, body, qs):
    existing = APP.cfg.find("targets", get_str(body, "id")) if body.get("id") else None
    target = clean_target(body, existing)
    out = run_client(target, ["status", "--output-format", "json"], timeout=45)
    try:
        data = json.loads(out)
    except ValueError:
        data = {}
    return {"ok": True, "total": data.get("total"), "used": data.get("used"), "avail": data.get("avail")}


@route("GET", r"/api/jobs")
def api_jobs(h, body, qs):
    return {"jobs": [public_job(j) for j in APP.cfg.snapshot()["jobs"]]}


@route("POST", r"/api/jobs")
def api_job_create(h, body, qs):
    j = clean_job(body, APP.cfg)
    with APP.cfg.lock:
        APP.cfg.data["jobs"].append(j)
        APP.cfg.save()
    APP.scheduler.reset(j["id"])
    return {"job": public_job(j)}


@route("PUT", r"/api/jobs/(\w+)")
def api_job_update(h, body, qs, jid):
    existing = APP.cfg.find("jobs", jid)
    if not existing:
        raise ApiError(404, "That job doesn't exist.")
    j = clean_job(body, APP.cfg, existing)
    with APP.cfg.lock:
        APP.cfg.data["jobs"] = [j if x["id"] == jid else x for x in APP.cfg.data["jobs"]]
        APP.cfg.save()
    APP.scheduler.reset(jid)
    return {"job": public_job(j)}


@route("DELETE", r"/api/jobs/(\w+)")
def api_job_delete(h, body, qs, jid):
    if APP.runs.list(jid, limit=1, statuses=("queued", "running")):
        raise ApiError(409, "This job is running. Cancel the run before deleting the job.")
    with APP.cfg.lock:
        APP.cfg.data["jobs"] = [x for x in APP.cfg.data["jobs"] if x["id"] != jid]
        APP.cfg.save()
    APP.scheduler.reset(jid)
    return {"ok": True}


@route("POST", r"/api/jobs/(\w+)/run")
def api_job_run(h, body, qs, jid):
    run, created = APP.runner.enqueue(jid, "manual")
    return {"run": run, "created": created}


@route("GET", r"/api/jobs/(\w+)/snapshots")
def api_job_snapshots(h, body, qs, jid):
    job = APP.cfg.find("jobs", jid)
    if not job:
        raise ApiError(404, "That job doesn't exist.")
    target = APP.cfg.find("targets", job["target_id"])
    if not target:
        raise ApiError(400, "This job has no destination.")
    out = run_client(target, ["snapshot", "list", f"host/{job['backup_id']}", "--output-format", "json"], timeout=60)
    try:
        items = json.loads(out)
    except ValueError:
        items = []
    snaps = []
    for s in items if isinstance(items, list) else []:
        verify = s.get("verification") or {}
        snaps.append({
            "time": s.get("backup-time"),
            "size": s.get("size"),
            "files": [f.get("filename") if isinstance(f, dict) else f for f in s.get("files", [])],
            "verified": verify.get("state"),
            "protected": s.get("protected", False),
            "comment": s.get("comment"),
        })
    snaps.sort(key=lambda s: s["time"] or 0, reverse=True)
    return {"snapshots": snaps, "group": f"host/{job['backup_id']}"}


@route("GET", r"/api/runs")
def api_runs(h, body, qs):
    job = (qs.get("job") or [None])[0]
    limit = to_int((qs.get("limit") or [100])[0], "limit", 1, 1000)
    return {"runs": APP.runs.list(job, limit=limit)}


@route("GET", r"/api/runs/(\w+)")
def api_run(h, body, qs, rid):
    run = APP.runs.get(rid)
    if not run:
        raise ApiError(404, "That run doesn't exist.")
    return {"run": run}


@route("GET", r"/api/runs/(\w+)/log")
def api_run_log(h, body, qs, rid):
    run = APP.runs.get(rid)
    if not run:
        raise ApiError(404, "That run doesn't exist.")
    offset = to_int((qs.get("offset") or [0])[0], "offset", 0, 2 ** 40)
    path = os.path.join(LOG_DIR, f"{rid}.log")
    text, new_offset, truncated = "", offset, False
    if os.path.exists(path):
        size = os.path.getsize(path)
        limit = 512 * 1024
        if offset == 0 and size > limit:
            offset, truncated = size - limit, True
        with open(path, "rb") as f:
            f.seek(offset)
            chunk = f.read(limit)
        text = chunk.decode("utf-8", "replace")
        new_offset = offset + len(chunk)
    return {"text": text, "offset": new_offset, "truncated": truncated,
            "done": run["status"] not in ("queued", "running"), "run": run}


@route("POST", r"/api/runs/(\w+)/cancel")
def api_run_cancel(h, body, qs, rid):
    APP.runner.cancel(rid)
    return {"ok": True}


@route("GET", r"/api/email")
def api_email(h, body, qs):
    return {"email": public_email(APP.cfg.snapshot()["email"])}


@route("PUT", r"/api/email")
def api_email_update(h, body, qs):
    with APP.cfg.lock:
        e = clean_email(body, APP.cfg.data["email"])
        APP.cfg.data["email"] = e
        APP.cfg.save()
    return {"email": public_email(e)}


@route("POST", r"/api/email/test")
def api_email_test(h, body, qs):
    existing = APP.cfg.snapshot()["email"]
    candidate = dict(body)
    candidate["enabled"] = True
    e = clean_email(candidate, existing)
    try:
        APP.notifier.send(
            f"[PBS Manager] Test email from {HOSTNAME}",
            f"This is a test from PBS Backup Manager on {HOSTNAME}.\n\n"
            "If you received it, backup alerts will reach this address.\n", settings=e)
    except Exception as exc:
        raise ApiError(502, f"Couldn't send the email: {exc}")
    return {"ok": True}


@route("GET", r"/api/browse")
def api_browse(h, body, qs):
    path = (qs.get("path") or ["/srv"])[0] or "/"
    path = os.path.normpath(path if path.startswith("/") else "/" + path)
    if not os.path.isdir(path):
        path = "/"
    dirs = []
    try:
        with os.scandir(path) as it:
            for entry in it:
                try:
                    if entry.is_dir(follow_symlinks=True):
                        dirs.append(entry.name)
                except OSError:
                    continue
    except PermissionError:
        raise ApiError(403, f"Can't read {path}.")
    dirs.sort(key=str.lower)
    parent = os.path.dirname(path) if path != "/" else None
    return {"path": path, "parent": parent, "dirs": dirs[:1000]}


# --------------------------------------------------------------------------
# HTTP server
# --------------------------------------------------------------------------

class Handler(BaseHTTPRequestHandler):
    server_version = "PBSManager/" + VERSION
    sys_version = ""
    protocol_version = "HTTP/1.1"
    timeout = 120

    def log_message(self, fmt, *args):
        log.debug("%s %s", self.client_address[0], fmt % args)

    def do_GET(self):
        self._dispatch("GET")

    def do_POST(self):
        self._dispatch("POST")

    def do_PUT(self):
        self._dispatch("PUT")

    def do_DELETE(self):
        self._dispatch("DELETE")

    def client_ip(self):
        peer = self.client_address[0]
        if not APP.is_trusted(peer):
            return peer
        xff = self.headers.get("X-Forwarded-For")
        if xff:
            hops = [hop.strip() for hop in xff.split(",") if hop.strip()]
            for hop in reversed(hops):
                if not APP.is_trusted(hop):
                    return hop
            if hops:
                return hops[0]
        real = self.headers.get("X-Real-IP")
        return real.strip() if real else peer

    def is_https(self):
        if getattr(self.server, "tls", False):
            return True
        if APP.is_trusted(self.client_address[0]):
            proto = (self.headers.get("X-Forwarded-Proto") or "").split(",")[0].strip().lower()
            return proto == "https"
        return False

    def session_token(self):
        raw = self.headers.get("Cookie")
        if not raw:
            return None
        jar = cookies.SimpleCookie()
        try:
            jar.load(raw)
        except cookies.CookieError:
            return None
        morsel = jar.get(SESSION_COOKIE)
        return morsel.value if morsel else None

    def _dispatch(self, method):
        self.set_cookie = None
        try:
            parsed = urlparse(self.path)
            path = parsed.path
            qs = parse_qs(parsed.query)
            base = APP.base_path
            if base:
                if path == base:
                    return self._redirect(base + "/" + (f"?{parsed.query}" if parsed.query else ""))
                if path.startswith(base + "/"):
                    path = path[len(base):]
            if method == "GET" and path in ("/", "/index.html"):
                return self._send_static("index.html", "text/html; charset=utf-8")
            if not path.startswith("/api/"):
                raise ApiError(404, "Not found.")
            if method != "GET" and self.headers.get("X-PBSM") != "1":
                raise ApiError(403, "Missing request header.")
            for m, pattern, fn, needs_auth in ROUTES:
                if m != method:
                    continue
                match = pattern.fullmatch(path)
                if not match:
                    continue
                if needs_auth and not APP.session_user(self.session_token()):
                    raise ApiError(401, "Sign in to continue.")
                body = self._read_json() if method in ("POST", "PUT") else {}
                return self._send_json(200, fn(self, body, qs, *match.groups()))
            raise ApiError(404, "Not found.")
        except ApiError as exc:
            self._send_json(exc.status, {"error": exc.message})
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception:
            log.exception("Request failed: %s %s", method, self.path)
            self._send_json(500, {"error": "Something went wrong on the server. Check the service log."})

    def _redirect(self, location):
        self.send_response(301)
        self.send_header("Location", location)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _read_json(self):
        length = int(self.headers.get("Content-Length") or 0)
        if length > 1_000_000:
            raise ApiError(413, "Request too large.")
        raw = self.rfile.read(length) if length else b""
        if not raw:
            return {}
        try:
            data = json.loads(raw)
        except ValueError:
            raise ApiError(400, "Invalid JSON.")
        if not isinstance(data, dict):
            raise ApiError(400, "Expected a JSON object.")
        return data

    def _security_headers(self):
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Cache-Control", "no-store")

    def _cookie_header(self):
        if self.set_cookie is None:
            return
        secure = "; Secure" if self.is_https() else ""
        if self.set_cookie:
            value = f"{SESSION_COOKIE}={self.set_cookie}; Path=/; HttpOnly; SameSite=Strict; Max-Age={SESSION_TTL}{secure}"
        else:
            value = f"{SESSION_COOKIE}=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0{secure}"
        self.send_header("Set-Cookie", value)

    def _send_json(self, status, data):
        payload = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self._security_headers()
        self._cookie_header()
        self.end_headers()
        self.wfile.write(payload)

    def _send_static(self, name, ctype):
        with open(os.path.join(STATIC_DIR, name), "rb") as f:
            payload = f.read()
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("Content-Security-Policy",
                         "default-src 'self'; script-src 'self' 'unsafe-inline'; "
                         "style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
        self._security_headers()
        self.end_headers()
        self.wfile.write(payload)


class Server(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True
    ssl_context = None
    tls = False

    def finish_request(self, request, client_address):
        # TLS handshake happens here, in the per-connection thread, so a slow
        # client can't block the accept loop.
        if self.ssl_context:
            try:
                request = self.ssl_context.wrap_socket(request, server_side=True)
            except (ssl.SSLError, OSError):
                return
        self.RequestHandlerClass(request, client_address, self)


# --------------------------------------------------------------------------
# CLI
# --------------------------------------------------------------------------

def cmd_serve(args):
    global APP
    logging.basicConfig(level=logging.DEBUG if args.debug else logging.INFO,
                        format="%(asctime)s %(levelname)s %(message)s")
    APP = App()
    srv_cfg = APP.cfg.data["server"]
    bind = args.bind or srv_cfg.get("bind", "0.0.0.0")
    port = args.port or int(srv_cfg.get("port", 8099))
    server = Server((bind, port), Handler)
    cert, key = srv_cfg.get("tls_cert"), srv_cfg.get("tls_key")
    if cert and key:
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.minimum_version = ssl.TLSVersion.TLSv1_2
        ctx.load_cert_chain(cert, key)
        server.ssl_context = ctx
        server.tls = True
    scheme = "https" if server.tls else "http"
    log.info("PBS Backup Manager %s listening on %s://%s:%d", VERSION, scheme, bind, port)
    if APP.trusted_nets:
        log.info("Trusting X-Forwarded-* headers from: %s", ", ".join(map(str, APP.trusted_nets)))
    if APP.base_path:
        log.info("Serving under base path %s/", APP.base_path)
    if not APP.cfg.data["auth"].get("password_hash"):
        log.warning("No admin password set. Run: sudo pbs-manager passwd")

    def stop(*_):
        threading.Thread(target=server.shutdown, daemon=True).start()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    server.serve_forever()


def cmd_passwd(args):
    cfg = Config(CONFIG_PATH)
    if args.password_stdin:
        password = sys.stdin.readline().rstrip("\n")
    else:
        import getpass
        password = getpass.getpass("New admin password: ")
        if password != getpass.getpass("Repeat it: "):
            sys.exit("Passwords don't match.")
    if len(password) < 10:
        sys.exit("Use at least 10 characters.")
    with cfg.lock:
        if args.username:
            cfg.data["auth"]["username"] = args.username
        cfg.data["auth"]["password_hash"] = hash_password(password)
        cfg.save()
    print(f"Password set for user '{cfg.data['auth']['username']}'. Restart the service to sign out existing sessions.")
    if cfg.data["auth"].get("totp_secret"):
        print("Two-step verification is still on. If you've also lost your authenticator, run: sudo pbs-manager totp-reset")


def cmd_configure(args):
    cfg = Config(CONFIG_PATH)
    with cfg.lock:
        s = cfg.data["server"]
        if args.bind is not None:
            s["bind"] = args.bind
        if args.port is not None:
            s["port"] = args.port
        if args.tls_cert is not None:
            s["tls_cert"] = args.tls_cert
        if args.tls_key is not None:
            s["tls_key"] = args.tls_key
        if args.max_concurrent is not None:
            cfg.data["settings"]["max_concurrent"] = max(1, args.max_concurrent)
        if args.trusted_proxies is not None:
            items = [x.strip() for x in args.trusted_proxies.split(",") if x.strip()]
            for item in items:
                try:
                    ipaddress.ip_network(item, strict=False)
                except ValueError:
                    sys.exit(f"Not an IP address or network: {item}")
            s["trusted_proxies"] = items
        if args.base_path is not None:
            s["base_path"] = normalize_base_path(args.base_path)
        cfg.save()
    print(json.dumps(cfg.data["server"], indent=2))
    print("Restart the service to apply: sudo systemctl restart pbs-manager")


def cmd_totp_reset(args):
    cfg = Config(CONFIG_PATH)
    with cfg.lock:
        cfg.data["auth"].update(totp_secret="", totp_last_step=0, recovery_codes=[])
        cfg.save()
    print("Two-step verification is off. Sign in with your password and set it up again from Account.")


def main():
    parser = argparse.ArgumentParser(prog="pbs-manager", description="PBS Backup Manager")
    sub = parser.add_subparsers(dest="cmd")
    p = sub.add_parser("serve", help="Run the web UI and scheduler")
    p.add_argument("--bind")
    p.add_argument("--port", type=int)
    p.add_argument("--debug", action="store_true")
    p.set_defaults(func=cmd_serve)
    p = sub.add_parser("passwd", help="Set the admin password")
    p.add_argument("--username")
    p.add_argument("--password-stdin", action="store_true")
    p.set_defaults(func=cmd_passwd)
    p = sub.add_parser("configure", help="Change server settings")
    p.add_argument("--bind")
    p.add_argument("--port", type=int)
    p.add_argument("--tls-cert")
    p.add_argument("--tls-key")
    p.add_argument("--max-concurrent", type=int)
    p.add_argument("--trusted-proxies", help="Comma-separated IPs/networks whose X-Forwarded-* headers are trusted ('' to clear)")
    p.add_argument("--base-path", help="Serve under a sub-path such as /backups ('' for none)")
    p.set_defaults(func=cmd_configure)
    p = sub.add_parser("totp-reset", help="Turn off two-step verification if you've lost your device")
    p.set_defaults(func=cmd_totp_reset)
    args = parser.parse_args()
    if not getattr(args, "func", None):
        parser.print_help()
        sys.exit(1)
    args.func(args)


if __name__ == "__main__":
    main()
