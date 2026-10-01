# CLAUDE.md

Context for Claude Code sessions working on this repository.

## What this is

PBS Backup Manager: a self-hosted web UI that runs file-level backups to Proxmox Backup Server with `proxmox-backup-client`. The owner runs it on OpenMediaVault, backing up shares under `/srv/dev-disk-by-uuid-…/` to a PBS server on the LAN, with jobs like `omv-media` and `omv-data`. It's installed with `sudo ./install.sh` as a systemd service (`pbs-manager`), often behind a reverse proxy, with two-step sign-in on.

## Hard rules

- `app.py` and `qr.py` use the **Python standard library only** (3.8+). No pip dependencies, no build step. Tests may use optional extras but must skip cleanly without them.
- The UI is one file, `static/index.html`: vanilla JS, no frameworks, no CDNs (it must work offline on a LAN and under a strict CSP). Escape all user content with `esc()`.
- **Credentials are write-only.** Token secrets, the SMTP password and key file passwords are never returned by the API (see the `public_*` functions) and never included in exports. Empty values on update keep the stored secret.
- Never pass credentials on a command line; the client gets them through `PBS_*` environment variables.
- Settings changes must keep working for existing installs. The installer upgrades in place and must not reset settings.

## Where things are

- `app.py`: config (`Config`), validation (`clean_*`), scheduling (`next_run_time`, `Scheduler`), backups (`build_backup_cmd`, `client_env`, `Runner`), sizes (`SizeTracker`), email (`Notifier`), export/import, HTTP routes (`@route`), CLI (`main`).
- `tests/`: `unittest`. `tests/helpers.py` has `AppServer`, `Client`, `MiniSMTP` and `wait_for`; `tests/fake_client.py` stands in for `proxmox-backup-client`.
- `docs/`: user and developer documentation. `docs/development.md` explains the architecture.

## Commands

```bash
make test       # python3 -m unittest discover -s tests -t .
make lint       # ruff + shellcheck
make check      # both
```

Run the app locally without a PBS server:

```bash
export PBSM_CONFIG_DIR=/tmp/pbsm/conf PBSM_DATA_DIR=/tmp/pbsm/data PBSM_CLIENT=$PWD/tests/fake_client.py
python3 app.py passwd && python3 app.py serve --port 8099 --bind 127.0.0.1 --debug
```

## How to make changes

1. Work on a branch; open a pull request against `main`.
2. Add or update tests with every behavior change. New endpoints get coverage in `tests/test_api.py`.
3. Keep `make check` green.
4. Update the relevant page in `docs/` and add an entry under an `## [Unreleased]` heading in `CHANGELOG.md`.
5. User-facing text is plain and specific: say what happened and what to do next. No jargon in the UI.

## Releasing

Version lives in `VERSION` in `app.py`. To release X.Y.Z, in a pull request bump `VERSION` and rename `## [Unreleased]` in `CHANGELOG.md` to `## [X.Y.Z] - YYYY-MM-DD` (add the link reference at the bottom). After merge, the owner tags `vX.Y.Z` on `main` and pushes the tag; `.github/workflows/release.yml` runs the tests, verifies the tag matches `VERSION`, and publishes the release with the package and checksum. See `docs/development.md`.
