# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/).

## [Unreleased]

Version 2: a rewrite in Go, renamed Proxmox Backup Client Web Manager. Work in progress; see docs/design-v2.md.

### Added
- One self-contained `pbcwm` program for Linux x86-64 and ARM64, with the web UI built in and settings in an SQLite database.
- First-run setup in the browser, protected by a one-time setup code that the installer prints (`sudo pbcwm setup-code` shows it again).
- Settings page covering everything that used to need `pbs-manager configure` or `config.json`: server name, sign-out time, listen address, port, HTTPS, trusted reverse proxies and base path.
- Network changes that could lock you out are tried first and kept only once you confirm them from the new address. Otherwise they're undone after about two minutes.
- HTTPS with a self-signed certificate (created by the server, no openssl needed), your own uploaded certificate, or off. Plain `http://` visits are redirected to `https://` on the same port.
- Sign-ins survive restarts and upgrades. Sessions are stored as hashes, and the two-step secret is encrypted on disk.
- `pbcwm network --reset` puts the network settings back to the defaults if the web UI can't be reached.
- Installer for Debian 12/13, including LXC containers. It runs the service as an unprivileged `pbcwm` user with systemd sandboxing, and falls back gracefully in containers without nesting.

### Changed
- The UI runs under a strict Content Security Policy with no inline scripts or styles.

### Kept from 1.x
- Two-step verification with QR enrolment, recovery codes and replay protection.
- Sign-in throttling per address and for the whole account.
- Password hashes, so 1.x passwords will carry over when settings import arrives.

## [1.2.0] - 2026-09-30

First public release.

### Added
- Dashboard **Data protected** widget: total size of the folders you back up, measured in the background with `du` at idle priority, compared with the size of your latest backups.
- Dashboard **Destination space** widget: free and used space for each destination, refreshed every 15 minutes, with amber and red warnings at 80% and 90% used.
- Folder size and latest backup size for each job on the overview and job pages.
- Destination usage shown automatically on the Destinations page.
- **Export and import settings** (Account page, plus `pbs-manager export` and `pbs-manager import`). Exports contain no credentials; importing keeps secrets already saved for matching destinations and jobs and lists what needs to be entered.
- Backups refuse to start, with a clear message, when a destination has no token secret saved.
- Test suite (unit, HTTP integration and optional browser tests), GitHub Actions workflow, linting, and full documentation under `docs/`.

### Changed
- Background threads stop cleanly on shutdown.

## 1.1.0 - 2026-09-30 (unpublished)

### Added
- Two-step verification (TOTP) with QR enrollment, single-use recovery codes and replay protection.
- Username can be changed from the Account page.
- Reverse-proxy support: trusted `X-Forwarded-For`/`X-Forwarded-Proto`, sub-path hosting (`--base-path`), `/api/health`.
- Installer options `--behind-proxy`, `--proxy-ip` and `--base-path`; re-running the installer keeps existing settings.
- Account-wide sign-in lockout after repeated failures, in addition to per-address throttling.
- `pbs-manager totp-reset` command.

## 1.0.0 - 2026-09-30 (unpublished)

### Added
- Initial release: destinations, backup jobs with schedules, folder browser, exclusions, speed limits, encryption keys, live logs, run cancellation, snapshot listing, email failure alerts, HTTPS with a self-signed certificate, systemd installer.

[1.2.0]: https://github.com/bradyloveland/pbswebclient/releases/tag/v1.2.0
