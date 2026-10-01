# PBS Backup Manager 2: design

Status: **draft for review**. Nothing here is built yet.

Version 2 is a rewrite in Go. It turns the single-machine app (1.x) into a central server that manages backups on many Linux machines over SSH, sending each one to one or more Proxmox Backup Server (PBS) destinations. Everything 1.2.0 does keeps working.

## Goals

1. **Central server** with a web UI, designed to run in a Debian LXC (or any Debian/Ubuntu machine or VM).
2. **Clients over SSH.** The server connects to each client, checks for `proxmox-backup-client`, installs it if it's missing, and runs backups there. No agent.
3. **Many destinations and many clients.** Each job backs up one client to one or more destinations.
4. **Everything from the browser** after the first install: setup, all settings (nothing only in a config file), and updates. Updates come either from a button that downloads the latest GitHub release, or from uploading a release file.
5. **Keep every 1.2.0 feature** (see [Feature parity](#feature-parity)).

## Non-goals (for now)

- A client agent. Every client must be reachable by SSH from the server: on the LAN or over a VPN such as Tailscale, NetBird or WireGuard.
- Exposing the server or clients to the public internet. It's supported behind a reverse proxy, but the design assumes a private network.
- Windows or macOS clients (PBS has no client for them).
- Restoring from the web UI. Restores stay in the PBS web interface or `proxmox-backup-client restore`, as in 1.x. This could be a later 2.x feature.

## Versions and branches

- `v2` holds the rewrite until 2.0.0 is ready. `main` stays on 1.2.0 until then.
- When 2.0.0 ships, `v2` merges into `main`. A `v1` branch is kept in case 1.x needs a fix.
- After 2.0.0: **bug fixes** are patch releases (2.0.1, 2.0.2, …) and **features and enhancements** are minor releases (2.1.0, 2.2.0, …).
- Releases are still published by GitHub Actions from a `vX.Y.Z` tag, as in 1.x.

## Supported clients

These match what `proxmox-backup-client` supports:

| Client | How the client software is installed |
| --- | --- |
| Debian 13 (trixie), 12 (bookworm) | Proxmox `pbs-client` apt repository, package `proxmox-backup-client` |
| Debian 11 (bullseye), 10 (buster) | Same repository, older client versions. The UI warns that these are older. |
| Debian-based (Ubuntu, OMV, Proxmox VE hosts) | Already present on Proxmox VE. Otherwise the matching Debian repository if the base is known, or the static build |
| Any other x86-64 Linux | Static build (`proxmox-backup-client-static`): the server downloads it, takes the single executable out, and copies it to the client |
| Non-x86-64 (ARM etc.) | Not supported. The UI says so plainly. |

Detection reads `/etc/os-release` and `uname -m` over SSH. If a client already has the software, the server records its version and leaves it alone.

## Architecture

```
Browser ── HTTPS ──> pbs-manager (one Go executable, runs as user pbs-manager)
                       ├─ HTTP API + embedded web UI
                       ├─ SQLite database (settings, clients, destinations, jobs, runs)
                       ├─ Scheduler ──> Runner ──SSH──> client: systemd-run proxmox-backup-client backup …
                       ├─ Client manager: SSH keys, host keys, detect/install client software, folder browser
                       ├─ Size tracker: du over SSH; datastore status and snapshot lists run on the server
                       ├─ Notifier: SMTP alerts
                       └─ Updater: GitHub release check, upload, verify, swap, restart, roll back
```

- **One executable**, with the web UI built in (`go:embed`). It's written in pure Go so it builds for x86-64 and ARM64 with no C compiler.
- **Dependencies kept small:** `golang.org/x/crypto/ssh` and a pure-Go SQLite driver (`modernc.org/sqlite`). Anything else needs a reason.
- **Web UI:** still plain JavaScript with no framework and no CDNs, so it works offline under a strict CSP. It's split into a few files instead of one, all embedded in the executable.
- **Runs as an unprivileged user,** `pbs-manager`, not root. The server never reads backup data itself. Even "this machine" is just a client reached over SSH to `127.0.0.1`, so every client works the same way.
- **The server's own copy of `proxmox-backup-client`,** installed in the LXC, is only used to talk to PBS (connection tests, datastore space, snapshot lists). It never reads files.

### Data model

| Table | Holds |
| --- | --- |
| `clients` | Name, address, SSH port and user, pinned host key, privilege mode (root or sudo), detected OS, CPU type and client version, last contact |
| `destinations` | As in 1.x: host, port, datastore, user, token name, token secret (encrypted), fingerprint. New: an optional PBS namespace |
| `jobs` | Client, folders (path and archive name), exclusions, schedule, change detection, speed limit, key file path and password (encrypted), enabled, **list of destinations**, optional namespace override |
| `runs` | One row per job, per destination, per start. Rows from the same start share a `group_id`. Status, times, exit code, summary, email error. |
| `run_logs` | Log text, or a pointer to a log file under the data folder |
| `settings` | Key/value pairs for everything on the Settings page |
| `auth`, `sessions` | Admin account, TOTP, recovery codes. Sessions survive a restart (1.x signs everyone out), so an update doesn't sign you out. |

Schema changes run as numbered migrations at startup. The updater backs up the database before applying a new version.

### Running a backup on a client

1. The scheduler or **Run now** creates one run per destination in the job. They run one after another, and each run has its own log, result and alert.
2. Over SSH, the server writes the credentials into a mode-600 file under `/run/pbs-manager/` on the client. This is a RAM-only folder, and the credentials are sent through the SSH connection's input, so they never appear in a command line or process list. The client reads them through `PBS_PASSWORD_FILE` and `PBS_ENCRYPTION_PASSWORD_FILE`. Proxmox documents both variables. Older clients, such as the Debian 10 package, get checked during implementation.
3. The server starts the backup as a temporary systemd unit (`systemd-run --unit=pbsm-<run id> --collect …`). Its output goes to `/var/log/pbs-manager/<run id>.log` on the client.
4. The server follows the log over SSH and copies it into its own store, so the live log view works as it does now.
5. When the unit finishes, the server reads the exit status, deletes the credential file and the client-side log, and records the result.
6. **Cancel** sends `systemctl kill --signal=SIGINT` to the unit, then `SIGKILL` after 20 seconds, as in 1.x.

Because the backup runs as a systemd unit on the client, **a dropped SSH connection or a restarted server doesn't stop it.** On startup the server finds runs that were in progress and reconnects to them. A run is only marked failed if its unit is gone and no result was recorded.

**Limits:** one backup at a time per client by default, plus a server-wide maximum. Both are on the Settings page.

### Several destinations per job

A job can send to more than one destination, one after another. The overview shows a result per destination.

The job form will point out that every extra destination reads every file on the client again. For an offsite copy, a **PBS sync job** (one PBS pulling from another) is usually lighter on the client.

### Clients and SSH

- **Key:** the server creates its own ed25519 key at first start. The client page shows the public key to copy.
- **Adding a client with no terminal:** enter the address and an SSH username and password **once**. The server installs its key in `authorized_keys`, checks that key sign-in works, and throws the password away. You can also paste the key in yourself.
- **Host keys:** the client's key fingerprint is shown the first time it connects, and you confirm it. After that it's pinned. If it changes, every action on that client stops with a clear explanation, until you accept the new key.
- **Privilege:** either root over SSH, or a normal user with passwordless `sudo`. The server detects which and runs commands accordingly. The docs explain how to limit the sudo rule.
- **Folder browser:** lists folders on the client over SSH. It works like 1.x, and starts at `/srv` when that exists.
- **Install client software:** the button is shown when a client doesn't have it. It runs the right method from [Supported clients](#supported-clients) and streams the output into the UI, like a backup log.

## Self-update

### Getting a release

- **Check for updates:** the Settings page shows the running version and the latest GitHub release, with its changelog. A daily automatic check can show a banner when a new version is out. It never installs on its own unless you turn on "Install updates automatically" and set a time window.
- **Upload:** you can upload a release `.tar.gz` downloaded from GitHub. This is for servers with no internet access.

### Checking a release

- Each release includes `SHA256SUMS` and an ed25519 signature of it, `SHA256SUMS.sig`. GitHub Actions signs them with a private key kept in a repository secret.
- The public key is built into the executable. The updater refuses a file whose signature or checksum doesn't match. An uploaded file is checked the same way.
- Installing an older version is allowed only as an explicit "Roll back to X" action.

### Installing a release

1. Wait until no backup is starting. Running backups are systemd units on the clients, so they aren't interrupted.
2. Back up the database to `backups/pbs-manager-<old version>-<time>.db`.
3. Write the new executable next to the old one. Keep the old one as `pbs-manager.prev`, then swap them in one step (rename).
4. Exit. systemd restarts the service (`Restart=always`), and the new version applies any database migrations.
5. If the new version doesn't report healthy within 60 seconds, or fails to start three times, systemd runs a small `pbs-manager-rollback` unit (through `OnFailure=`). It puts back `pbs-manager.prev` and the database backup. The UI then reports that the update was rolled back, and why.

The service user owns the install folder, so updating needs no root access.

**Private repository:** the GitHub API only shows releases of a private repo to someone with a token. Either the repo becomes public, or the Settings page takes a read-only GitHub token, stored write-only like other secrets. See [Open questions](#open-questions).

## Settings in the browser

Every setting that was CLI-only or config-file-only in 1.x moves to a **Settings** page:

| Setting | 1.x location | Notes |
| --- | --- | --- |
| Listen address and port | `pbs-manager configure` | See "Network changes" below |
| HTTPS certificate | `configure --tls-cert/--tls-key` | Upload your own cert and key, or regenerate the self-signed one, or turn HTTPS off |
| Trusted reverse proxies | `configure --trusted-proxies` | |
| Base path (sub-path hosting) | `configure --base-path` | |
| Backups at once | `configure --max-concurrent` | Server-wide, plus per client |
| Run history kept | `config.json` only | |
| Size check intervals | Hard-coded | Folder size, snapshot and destination space intervals |
| Updates | (new) | Automatic check, automatic install and time window, GitHub token |
| SSH | (new) | Server public key, regenerate key, connection timeout |

**Network changes.** A wrong port, address or certificate can lock you out, so these changes work like a monitor resolution change:
1. The server starts listening with the new settings.
2. The UI sends you to the new address with a confirmation prompt.
3. If you don't confirm within 60 seconds, the server goes back to the old settings.

**What still needs a terminal:**
- The first install: one command in the LXC (see below).
- Recovery commands for when you're locked out: `pbs-manager passwd` and `pbs-manager totp-reset`. A browser can't help when you can't sign in.
- Import and export stay available as commands as well as in the UI.

## Install

- **One command** inside a fresh Debian 12/13 LXC downloads the latest release, checks its signature, and installs. It creates the `pbs-manager` user, the systemd units and the server's own `proxmox-backup-client`, then prints the URL and a **one-time setup code**.
- **First visit:** the browser asks for the setup code, then for an admin username and password. This replaces `sudo pbs-manager passwd`. The setup code stops someone else on the network from claiming a fresh install first.
- **Installer options:** port, `--behind-proxy`, `--proxy-ip` and `--base-path` keep working for scripted installs. All of them can be changed later in the UI.
- The docs will include a ready-made LXC recipe: an unprivileged container, a small disk, and no access to backup data needed.

## Moving from 1.x

- **Import a 1.2.0 settings export** (the JSON from Account, Export settings). The importer asks which client the 1.x jobs belong to, for example the OMV box, and creates that client if needed. Each job keeps its single destination. As in 1.x, the export has no credentials, so they're entered afterwards.
- **Also accept the 1.x `config.json` itself.** It contains the secrets, so nothing needs re-entering. The UI warns that this file holds credentials, and the server doesn't keep the uploaded file.
- Run history from 1.x isn't carried over.
- The docs will explain how to uninstall 1.x from the OMV box once its jobs run from the new server.

## Feature parity

Everything in 1.2.0, now per client:
- **Destinations:** connection test, datastore space with warnings at 80% and 90% used, fingerprint, token or password sign-in.
- **Jobs:**
  - multiple folders, with archive names
  - exclusions, change detection mode, speed limit, encryption key file and password
  - backup ID (defaults to the client's hostname)
  - schedules (by hand, certain days, every few hours); missed runs are skipped and a job never runs twice at once
  - pause/resume
- **Running:** Run now, cancel, live log, error summary, last 20 runs strip, Activity page, snapshot list.
- **Sizes:** folder sizes (`du` at idle priority, with a fallback), latest backup size, Data protected and Destination space widgets.
- **Email:** alerts on failure, success, or interruption; test email; STARTTLS, SSL or no encryption.
- **Account:**
  - password and two-step sign-in: TOTP with QR enrollment, recovery codes, replay protection
  - username change
  - sign-in throttling per address and for the whole account
- **Network:** HTTPS with a self-signed certificate, reverse proxy headers, base path, `/api/health`.
- **Settings:** export and import, with no credentials in exports.
- **Security:** the 1.x rules carry over:
  - secrets are write-only and never on a command line
  - the `X-PBSM` header plus SameSite cookies stop cross-site requests
  - strict CSP, all user content escaped
  - plain, specific messages for users

**New:** clients, several destinations per job, PBS namespaces (passed as `PBS_NAMESPACE`, so many clients can share one datastore neatly), client software install, self-update, the Settings page, setup in the browser, sessions that survive restarts, and secrets encrypted on disk. Encryption uses a key file kept separate from the database, so a copied database file alone doesn't reveal credentials.

## Testing

- `go test ./...` covers validation, scheduling, command building, export/import, auth and TOTP (with the RFC 6238 test values), and QR codes, ported from the 1.x tests.
- **SSH tests** use an SSH server started inside the test process. It runs commands in a temporary folder, together with a Go version of the 1.x fake client (fails on `fail`, runs slowly on `slow`, and so on). This tests the whole path (connect, host key pinning, start unit, follow log, cancel, reconnect after a restart) without real machines.
- **Container test in CI:**
  - start a Debian 12 container and a Debian 13 container with SSH and systemd
  - have the server install the real `proxmox-backup-client` from Proxmox's repository and confirm `version` works
  - no PBS server is needed for this
- **Updater test:** build two versions, sign them with a test key, update from one to the other, and check rollback with a broken build.
- **Browser smoke test:** Playwright, as in 1.x, kept optional.
- **Checks:** `gofmt`, `go vet`, `staticcheck` and `shellcheck` (for the installer and the client install scripts). These replace `ruff`.

## Milestones

| Milestone | Delivers |
| --- | --- |
| **M1: base** | Go project, SQLite and migrations, auth (password, TOTP, recovery codes, throttling), setup in the browser, Settings page, embedded UI shell, CI, installer |
| **M2: clients** | SSH key, add a client with a one-time password, host key pinning, detect and install the client software, folder browser |
| **M3: backups** | Destinations, jobs with several destinations, scheduler, remote runs as systemd units, live logs, cancel, reconnect, snapshots, email alerts |
| **M4: dashboard** | Overview across clients, size widgets, Activity page, banners |
| **M5: updates** | Signed releases, update check, upload, swap, health check, rollback |
| **M6: moving from 1.x and release** | 1.x import, docs, LXC recipe, testing on your machines → **2.0.0** |

Each milestone ends with passing tests and a pull request into `v2`, so you can try it in a test LXC as it grows.

## Open questions

1. **Public or private repository?** Update checks from inside the app need either a public repo or a GitHub token saved in Settings. If the repo stays private, every install needs a token.
2. **Root or sudo on clients?** The design supports both. Which should the UI suggest by default? A user with passwordless sudo is safer. Root is simpler and matches how 1.x runs today.
3. **Automatic installs:** should "Install updates automatically" be offered at all, or only notify you?
4. **Name:** keep "PBS Backup Manager" and the `pbswebclient` repo name?
