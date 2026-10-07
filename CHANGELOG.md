# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/).

## [Unreleased]

## [2.4.0] - 2026-10-07

### Added
- **Update a client's proxmox-backup-client from the web UI.** Point at "update to … available" in the Clients list and click **Update now**, or use the button on the client's page or the Updates page. The client installs only that package, at the version shown, and never removes anything. It won't start while one of the client's backups is running. A new major version still has to be installed on the machine itself, because it usually comes with an OS upgrade.

## [2.3.0] - 2026-10-01

### Added
- The job form lists other disks mounted inside the chosen folders, such as a Raspberry Pi's `/boot/firmware`, a USB drive or a network share. Backups don't include them, and **Add as a folder** adds one as an archive of its own. When `/` is chosen, it adds the usual excludes (swap files, downloaded packages, temporary files) as lines you can edit ([#33](https://github.com/bradyloveland/pbcmanager/issues/33)).
- Running backups show a progress bar with the percentage done and the time left on the Dashboard, the Backup jobs list, the client's page and the run page. The total is measured at the start of each run, using the job's excludes, at idle priority alongside the backup. Until then the previous run's size is used ([#34](https://github.com/bradyloveland/pbcmanager/issues/34)).

## [2.2.1] - 2026-10-01

### Fixed
- Updates from the web UI now install every file in the release, going by its signed file list. Before, the running version installed a fixed list of files, so the update from 2.1.0 to 2.2.0 left out the ARM64 runner and ARM64 clients couldn't be set up ([#30](https://github.com/bradyloveland/pbcmanager/issues/30)). Going back to the previous version also removes files the update added.
- The Updates page says when files of the running version are missing and can reinstall it from GitHub to put them back.

## [2.2.0] - 2026-10-01

### Added
- **ARM64 clients, such as a Raspberry Pi** with a 64-bit OS based on Debian 13. Setup sees the client's CPU type, sends the matching `pbcm-runner`, and installs Proxmox's own ARM64 `proxmox-backup-client` from the `test` component of its Debian 13 repository. An ARM64 machine on a Debian 12 based OS is refused with a clear message, because Proxmox doesn't build the client for it. Releases now include `pbcm-runner-arm64`, and each client gets signed runner updates for its own CPU type. ([#27](https://github.com/bradyloveland/pbcmanager/issues/27))

## [2.1.0] - 2026-10-01

### Added
- **Waiting proxmox-backup-client updates are shown** on the Clients list, the client page, the Dashboard and a new section of the Updates page. Each client checks its own package lists once a day, and when you press Check now or Repair. Nothing is installed: PBC Manager only reports it, and says when it's a new major version that usually comes with an OS upgrade. An optional email (off by default) reports an update that's been waiting for a number of days you choose. ([#12](https://github.com/bradyloveland/pbcmanager/issues/12))
- **HTML alert emails.** Each email has a coloured status bar (failed, succeeded, didn't run, can't reach, nearly full), a headline, a details table with the backup figures, the end of the log for failures, and a button to the run when the server's web address is set. A plain-text version goes with every email for mail apps that can't show HTML. Nothing loads from the internet: no web fonts, images or tracking. "Send plain-text emails only" under Alerts goes back to text only. The test email shows the new layout. ([#18](https://github.com/bradyloveland/pbcmanager/issues/18))
- **Backup figures** for every run: data read, new data uploaded (and compressed), data reused from the last backup, files (total and new or changed), and the upload time. They're read from `proxmox-backup-client`'s own summary, so nothing extra runs on the client. They appear in alert emails, with the destination's free space, on the run page (per folder when a job has several), and in the job page's run list. Runs from before 2.1.0 don't have them. ([#17](https://github.com/bradyloveland/pbcmanager/issues/17))
- **Dashboard metric cards:**
  - **Backup jobs:** how many, and how many are enabled, disabled and by hand only.
  - **Success rate:** over 7 and 30 days, with runs per day for the last 14 days.
  - **Largest backups** and **Longest running** (last 30 days): the top 5 of each.
  - Cancelled runs don't count towards the rate. A card says so when run history doesn't reach back over its whole period. A new `GET /api/metrics` provides the numbers. ([#14](https://github.com/bradyloveland/pbcmanager/issues/14))

### Changed
- **The Dashboard groups backup jobs under their client,** in sections you expand or collapse by clicking the client's header. Each header shows the client's status, a job counter (running, failing and disabled jobs included) and a **Client details** button. Your browser remembers which sections are open, and a client with a failing job or a connection problem always opens by itself. ([#13](https://github.com/bradyloveland/pbcmanager/issues/13))
- **Jobs have an Enabled switch** next to their name at the top of the job page, so a job can be turned on or off without opening Edit job. It replaces the job form's "Run on schedule" checkbox, which read oddly next to a "By hand only" schedule. A disabled job doesn't run, on schedule or with Run now, and the job lists say **Disabled** instead of "Paused". ([#10](https://github.com/bradyloveland/pbcmanager/issues/10))

## [2.0.1] - 2026-10-01

### Fixed
- **Adding an OpenMediaVault client failed after setup** because OMV only lets the `_ssh` group sign in over SSH (`AllowGroups root _ssh`). Setup now adds the `pbcm` account to an allowed group when sshd limits sign-ins by group. It never chooses one that grants admin rights, such as `sudo` or `docker`. When the limit is by user (`AllowUsers`) or a `Deny…` rule, setup leaves `sshd_config` alone and says exactly what to change. ([#9](https://github.com/bradyloveland/pbcmanager/issues/9))
- When the server can't sign in as `pbcm` after setup, the message now says what to do, including the OpenMediaVault command, without a doubled full stop.
- **Run now on a paused job.** The button was faded with the rest of the row, so it looked disabled, yet it still started a backup. A paused job now can't run at all: Run now is disabled, with a tooltip saying why, and the server refuses it too. Only the paused job's run history and next run are faded. ([#11](https://github.com/bradyloveland/pbcmanager/issues/11))

## [2.0.0] - 2026-10-01

Version 2: a rewrite in Go, renamed PBC Manager. One central server now manages backups on many clients over SSH; each client runs its own schedules and backs up straight to Proxmox Backup Server. To move from 1.x, see docs/guide/moving-from-1x.md.

### Added
- One self-contained `pbcm` program for Linux x86-64 and ARM64, with the web UI built in and settings in an SQLite database.
- First-run setup in the browser, protected by a one-time setup code that the installer prints (`sudo pbcm setup-code` shows it again).
- Settings page covering everything that used to need `pbs-manager configure` or `config.json`: server name, sign-out time, listen address, port, HTTPS, trusted reverse proxies and base path.
- Network changes that could lock you out are tried first and kept only once you confirm them from the new address. Otherwise they're undone after about two minutes.
- HTTPS with a self-signed certificate (created by the server, no openssl needed), your own uploaded certificate, or off. Plain `http://` visits are redirected to `https://` on the same port.
- Sign-ins survive restarts and upgrades. Sessions are stored as hashes, and the two-step secret is encrypted on disk.
- `pbcm network --reset` puts the network settings back to the defaults if the web UI can't be reached.
- Installer for Debian 12/13, including LXC containers. It runs the service as an unprivileged `pbcm` user with systemd sandboxing, and falls back gracefully in containers without nesting.

- **Clients**:
  - **Adding a client:** enter its address, check its SSH host key fingerprint, and sign in once as root or a sudo user. The password is used once and never saved.
  - **Supported clients:** Debian 12 and 13, and systems based on them (Proxmox VE 8/9, OpenMediaVault 7/8, Ubuntu 22.04/24.04). Older releases are refused with a clear message.
  - **Setup:** installs `proxmox-backup-client` from Proxmox if it's missing: the regular package on Debian, the static build on derivatives. The Proxmox signing keyring is checked against a built-in checksum.
  - **The `pbcm` account:** setup creates it so it signs in only with the server's key and can run nothing but `pbcm-runner`, through a forced command and a sudo rule checked with `visudo`.
  - **Host key pinning:** a client whose key changes is blocked until you compare the new key and repair it.
  - **Managing a client:** client details, Check now, a folder browser, Repair (runs setup again), and Remove (cleans everything up, or just takes the client off the list).
  - **The server's SSH key** is shown under Settings.

- **Backups**:
  - **Destinations:** PBS datastores with an optional namespace, a connection test showing free space, and write-only token secrets stored encrypted.
  - **Backup jobs:** per client, with several folders (picked with a folder browser on the client), exclusions, a schedule, change detection, a speed limit, an encryption key, and one or more destinations.
  - **Each client gets its jobs** as systemd timers and runs them itself, so backups carry on while the server is down. Credentials are encrypted with `systemd-creds` where available. Settings that couldn't be delivered are sent again automatically when the client is back.
  - **Run now and Cancel.** Cancel asks the backup client to stop cleanly.
  - **Run history** is collected from clients every 30 seconds while a backup runs and every 5 minutes otherwise, including runs from while the server was down. Finished logs are kept on the server.
  - **Pages:** a live log for each run, an Activity page across all clients, snapshot lists from PBS, and the Dashboard showing each job's last 20 runs.
  - **New settings** for how much run history clients and the server keep.

- **Email alerts** on a new Alerts page:
  - **Mail server settings:** SMTP with STARTTLS, SSL or none, an optional sign-in (the password is write-only and encrypted), several recipients, and a test email.
  - **Failed backups,** including interrupted ones, with the reason and the end of the log. **Successful backups** are optional.
  - **Missed backups:** a scheduled backup that didn't start within a grace period you set, judged once the server has heard from the client.
  - **Unreachable clients:** a client the server can't reach for longer than a time you set, and another email when it's back.
  - **Late reports:** alerts about a run the server only heard about late (it was down, or couldn't reach the client) say so.
  - **Each alert is sent once.** Recent alerts are listed with whether the email went out.
- **Client time zones:** clients report theirs, so next-run times and missed-backup checks follow the client's clock.

- **Sizes and space**:
  - **Data protected** on the Dashboard: how much data your jobs' folders hold, counting each folder once, and the size of the newest backups.
  - **Destination space** on the Dashboard and Destinations page, amber from 80% used and red from 90%.
  - **Folder sizes** are measured on each client in the background at low priority, every 12 hours and whenever a job's folders change. Missing folders are reported instead of counted as empty. Measure again starts a new measurement.
  - **Sizes per job:** each job shows its size, and its page shows the folders' size and the newest backup on each destination.
  - **Destination nearly full** email alert, at a percentage you choose (90% by default), and another email when there's room again.
  - **New settings** for how often space, backup sizes and folder sizes are checked.

- **Updates from the web UI** on a new Updates page:
  - **Check for updates** on GitHub, daily (with a notice on the Dashboard) and when you press Check now. Release notes are shown before you install.
  - **Download and install** in one click, or **upload a release file** for a server without internet access.
  - **Signed releases:** only files signed by the project are accepted. Each file's checksum is checked, and anything unexpected in the archive is refused.
  - **Safe installs:** the database is backed up first and the previous version is kept. If the new version stops with an error three times before it's settled in, the previous version and database are put back automatically, and the Updates page says why.
  - **Go back** to the previous version by hand.
  - **Automatic updates** (off by default) install new versions during an hour you choose. A version that was rolled back isn't tried again automatically.
  - **Client runners:** after the server updates, each client gets the matching `pbcm-runner`, and checks its signature before replacing itself. The Updates page shows each client's version.
- `pbcm rollback` puts back the previous version from a terminal, when the service is stopped.

- **Export and import** under Settings:
  - **Download settings:** destinations, jobs, alert settings and Settings page values. Passwords and token secrets are never included.
  - **Import from PBS Backup Manager 1.x:** from its settings export, or from its `config.json` with the credentials. Choose the client it ran on and see a preview first; missing secrets are asked for in the form.
  - **Import a PBC Manager export** on another server; jobs are matched to clients by address.
  - **Safe to repeat:** matching destinations are reused, jobs already there are skipped, and taken names are renamed.
  - **Imported jobs start paused,** so the old and new servers don't both back up the same folders.
- **User guide** in `docs/guide/`: installing (with an LXC recipe), setting up PBS tokens, clients, updates, moving from 1.x, and troubleshooting. A new README with screenshots.

- **"Web address of this server"** setting, so alert emails link to the run.

### Changed
- Renamed to **PBC Manager** (program `pbcm`, repository `bradyloveland/pbcmanager`).
- The web UI runs under a strict Content Security Policy with no inline scripts or styles, and browsers never run a stale script after an upgrade.

### Kept from 1.x
- Two-step verification with QR enrolment, recovery codes and replay protection.
- Sign-in throttling per address and for the whole account.
- The same password hashing as 1.x.

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

[Unreleased]: https://github.com/bradyloveland/pbcmanager/compare/v2.4.0...HEAD
[2.4.0]: https://github.com/bradyloveland/pbcmanager/compare/v2.3.0...v2.4.0
[2.3.0]: https://github.com/bradyloveland/pbcmanager/compare/v2.2.1...v2.3.0
[2.2.1]: https://github.com/bradyloveland/pbcmanager/compare/v2.2.0...v2.2.1
[2.2.0]: https://github.com/bradyloveland/pbcmanager/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/bradyloveland/pbcmanager/compare/v2.0.1...v2.1.0
[2.0.1]: https://github.com/bradyloveland/pbcmanager/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/bradyloveland/pbcmanager/releases/tag/v2.0.0
[1.2.0]: https://github.com/bradyloveland/pbcmanager/releases/tag/v1.2.0
