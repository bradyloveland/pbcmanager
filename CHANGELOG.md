# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/).

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
