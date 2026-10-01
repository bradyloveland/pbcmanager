# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/).

## [Unreleased]

Version 2: a rewrite in Go, renamed PBC Manager. Work in progress; see docs/design-v2.md.

### Added
- One self-contained `pbcm` program for Linux x86-64 and ARM64, with the web UI built in and settings in an SQLite database.
- First-run setup in the browser, protected by a one-time setup code that the installer prints (`sudo pbcm setup-code` shows it again).
- Settings page covering everything that used to need `pbs-manager configure` or `config.json`: server name, sign-out time, listen address, port, HTTPS, trusted reverse proxies and base path.
- Network changes that could lock you out are tried first and kept only once you confirm them from the new address. Otherwise they're undone after about two minutes.
- HTTPS with a self-signed certificate (created by the server, no openssl needed), your own uploaded certificate, or off. Plain `http://` visits are redirected to `https://` on the same port.
- Sign-ins survive restarts and upgrades. Sessions are stored as hashes, and the two-step secret is encrypted on disk.
- `pbcm network --reset` puts the network settings back to the defaults if the web UI can't be reached.
- Installer for Debian 12/13, including LXC containers. It runs the service as an unprivileged `pbcm` user with systemd sandboxing, and falls back gracefully in containers without nesting.

- **Clients** (milestone 2):
  - **Adding a client:** enter its address, check its SSH host key fingerprint, and sign in once as root or a sudo user. The password is used once and never saved.
  - **Supported clients:** Debian 12 and 13, and systems based on them (Proxmox VE 8/9, OpenMediaVault 7/8, Ubuntu 22.04/24.04). Older releases are refused with a clear message.
  - **Setup:** installs `proxmox-backup-client` from Proxmox if it's missing: the regular package on Debian, the static build on derivatives. The Proxmox signing keyring is checked against a built-in checksum.
  - **The `pbcm` account:** setup creates it so it signs in only with the server's key and can run nothing but `pbcm-runner`, through a forced command and a sudo rule checked with `visudo`.
  - **Host key pinning:** a client whose key changes is blocked until you compare the new key and repair it.
  - **Managing a client:** client details, Check now, a folder browser, Repair (runs setup again), and Remove (cleans everything up, or just takes the client off the list).
  - **The server's SSH key** is shown under Settings.

- **Backups** (milestone 3a):
  - **Destinations:** PBS datastores with an optional namespace, a connection test showing free space, and write-only token secrets stored encrypted.
  - **Backup jobs:** per client, with several folders (picked with a folder browser on the client), exclusions, a schedule, change detection, a speed limit, an encryption key, and one or more destinations.
  - **Each client gets its jobs** as systemd timers and runs them itself, so backups carry on while the server is down. Credentials are encrypted with `systemd-creds` where available. Settings that couldn't be delivered are sent again automatically when the client is back.
  - **Run now and Cancel.** Cancel asks the backup client to stop cleanly.
  - **Run history** is collected from clients every 30 seconds while a backup runs and every 5 minutes otherwise, including runs from while the server was down. Finished logs are kept on the server.
  - **Pages:** a live log for each run, an Activity page across all clients, snapshot lists from PBS, and the Dashboard showing each job's last 20 runs.
  - **New settings** for how much run history clients and the server keep.

- **Email alerts** (milestone 3b) on a new Alerts page:
  - **Mail server settings:** SMTP with STARTTLS, SSL or none, an optional sign-in (the password is write-only and encrypted), several recipients, and a test email.
  - **Failed backups,** including interrupted ones, with the reason and the end of the log. **Successful backups** are optional.
  - **Missed backups:** a scheduled backup that didn't start within a grace period you set, judged once the server has heard from the client.
  - **Unreachable clients:** a client the server can't reach for longer than a time you set, and another email when it's back.
  - **Late reports:** alerts about a run the server only heard about late (it was down, or couldn't reach the client) say so.
  - **Each alert is sent once.** Recent alerts are listed with whether the email went out.
- **Client time zones:** clients report theirs, so next-run times and missed-backup checks follow the client's clock.
- **New setting, "Web address of this server",** so alert emails link to the run.

### Changed
- The UI runs under a strict Content Security Policy with no inline scripts or styles.
- UI files are cache-busted by content, so browsers never run a stale script after an upgrade.
- The layout no longer overflows sideways on very narrow screens.
- Renamed to **PBC Manager** (program `pbcm`, repository `bradyloveland/pbcmanager`).
- The Overview page is now the **Dashboard**. It suggests two-step verification only while it's off.
- Folder browsing starts at `/`.

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
