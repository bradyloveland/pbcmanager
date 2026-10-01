# Development

## Layout

```
app.py              Server, API, scheduler, runner, size tracker, email, CLI
qr.py               QR code generator for two-step enrollment (standard library only)
static/index.html   The whole web UI: HTML, CSS and vanilla JavaScript
install.sh          Installer and upgrader
scripts/            Release helpers used by GitHub Actions
CLAUDE.md           Context for Claude Code sessions
uninstall.sh
tests/              Test suite (see below)
docs/               Documentation
.github/workflows/  CI
```

Everything uses the Python standard library so the app installs on a stock NAS with no package manager steps.

## Architecture

```
Browser ── HTTP(S) ──> Handler (ThreadingHTTPServer)
                         │  routes → api_* functions
                         ▼
                        App ──── Config (config.json, RLock, atomic writes)
                         │ ├──── RunStore (runs.json, logs/*.log)
                         │ ├──── Scheduler thread: computes next_run_time, enqueues due jobs every 15 s
                         │ ├──── Runner: queue + N worker threads → proxmox-backup-client subprocesses
                         │ ├──── SizeTracker thread: du / snapshot list / status, cached in sizes.json
                         │ └──── Notifier: SMTP alerts on finished runs
```

- **Configuration** is one JSON document guarded by a lock and written atomically (`write_json_atomic`: temp file, fsync, rename).
- **Validation** lives in the `clean_*` functions, which take untrusted input and return a normalized object or raise `ApiError(400, message)`. Messages are written for end users.
- **Secrets** are stripped by the `public_*` functions before anything is returned.
- **Backups** run `proxmox-backup-client` in a new session (so cancellation can signal the whole process group), with credentials passed in the environment, never on the command line. Output streams straight to the run's log file.
- **The scheduler** never catches up missed runs and never queues a job that is already queued or running.
- **Sizes** are cached; the dashboard never waits on PBS or the disk.

## Running locally

```bash
export PBSM_CONFIG_DIR=/tmp/pbsm/conf PBSM_DATA_DIR=/tmp/pbsm/data
export PBSM_CLIENT=$PWD/tests/fake_client.py      # optional: no real PBS needed
python3 app.py passwd
python3 app.py serve --port 8099 --bind 127.0.0.1 --debug
```

The fake client fails any backup whose folder path contains `fail`, runs slowly for paths containing `slow`, and reports fixed datastore and snapshot sizes.

## Tests

```bash
make test            # or: python3 -m unittest discover -s tests -t . -v
make lint            # ruff + shellcheck
```

| File | Covers |
| --- | --- |
| `test_auth.py` | Password hashing, TOTP against the RFC 6238 vectors, drift window, replay rejection, recovery codes |
| `test_qr.py` | QR structure (finder and timing patterns, valid format bits) and, with OpenCV installed, decoding across versions 1–15 |
| `test_validation.py` | Destination, job, schedule and email validation |
| `test_scheduling.py` | `next_run_time` for every schedule type |
| `test_backup_command.py` | Client command line and environment, error summaries, helpers |
| `test_export_import.py` | Exports contain no credentials; imports validate, keep local secrets and report what's missing |
| `test_sizes.py` | Folder measurement (including the no-`du` fallback) and dashboard totals |
| `test_api.py` | The real HTTP server end to end: auth, CSRF, throttling, TOTP sign-in, backups that succeed, fail, are cancelled or blocked, email alerts via a local SMTP server, export/import, proxy headers and base path |
| `test_release_tools.py` | Release notes extraction, packaging and version checks |
| `test_ui.py` | Browser smoke test (skipped unless Playwright is installed) |

Optional extras:

```bash
pip install opencv-python-headless numpy     # QR decode tests
pip install playwright && python -m playwright install chromium   # browser test
```

Integration tests start the app on a random port with temporary directories, so they never touch `/etc` or `/var`. The whole suite runs in about 40 seconds; most of that is deliberate one-second delays in the sign-in throttling tests.

## Making changes

- Keep the standard-library-only rule for `app.py` and `qr.py`.
- Add a test with every behavior change. New API endpoints belong in `test_api.py`.
- Write user-facing messages in plain language: what happened and what to do next.
- Update `docs/` and `CHANGELOG.md`.
- Bump `VERSION` in `app.py` for releases and tag `vX.Y.Z`.

## Releases

Releases are published by GitHub Actions (`.github/workflows/release.yml`) when a version tag is pushed. The workflow runs the tests, checks that the tag matches `VERSION` in `app.py`, builds `pbswebclient-X.Y.Z.tar.gz` with a SHA-256 checksum, and creates the GitHub release using that version's section of `CHANGELOG.md` as the notes.

1. In a pull request, bump `VERSION` in `app.py` and add a `## [X.Y.Z] - date` section to `CHANGELOG.md`.
2. Merge it once CI passes.
3. Tag the merge commit and push the tag:
   ```bash
   git checkout main && git pull
   git tag -a vX.Y.Z -m "PBS Backup Manager X.Y.Z"
   git push origin vX.Y.Z
   ```
4. Upgrade a test machine from the new release and run a backup.
