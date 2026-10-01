# PBC Manager 2: design

Status: **in progress**. Milestones 1, 2 and 3a are built; see the progress table in the README.

Version 2 is a rewrite in Go of PBS Backup Manager 1.x, under a new name: **PBC Manager**. It manages file-level backups on many Linux machines (clients) from one central server, sending each client's backups to one or more Proxmox Backup Server (PBS) destinations.

The key rule: **the server is only for managing and watching. Clients never depend on it.** Each client keeps its own schedule and credentials and backs up straight to PBS. If the server is down, offline or deleted, every client keeps backing up on schedule.

The project isn't affiliated with or endorsed by Proxmox Server Solutions GmbH. Proxmox is their trademark, and the README will say so.

## Goals

1. **Central server** with a web UI, designed to run in a Debian LXC (or any Debian/Ubuntu machine or VM).
2. **Clients run on their own.** Schedules, credentials and the backup program live on each client and run under systemd. The server sets them up, starts and cancels runs, and collects status and logs.
3. **Set up over SSH.** The server connects as root once, installs `proxmox-backup-client` if it's missing, and creates a limited service account. From then on it connects only with that account.
4. **Many destinations and many clients.** Each job backs up one client to one or more destinations.
5. **Everything from the browser** after the first install: setup, all settings (nothing only in a config file), and updates. Updates come from a button that downloads the latest GitHub release, or from uploading a release file.
6. **Keep every 1.2.0 feature** (see [Feature parity](#feature-parity)).

## Non-goals (for now)

- A resident agent: nothing on a client runs all the time or listens on a port. (The small program the server installs on clients only runs when systemd or an SSH command starts it. See [What gets installed on a client](#what-gets-installed-on-a-client).)
- Exposing the server or clients to the public internet. Clients must be reachable by SSH from the server, on the LAN or over a VPN such as Tailscale, NetBird or WireGuard. The server works behind a reverse proxy, but the design assumes a private network.
- Windows or macOS clients (PBS has no client for them).
- Restoring from the web UI. Restores stay in the PBS web interface or `proxmox-backup-client restore`. This could be a later 2.x feature.

## Versions and branches

- `v2` holds the rewrite until 2.0.0 is ready. `main` stays on 1.2.0 until then.
- When 2.0.0 ships, `v2` merges into `main`, and a `v1` branch is kept in case 1.x needs a fix.
- After 2.0.0: **bug fixes** are patch releases (2.0.1, 2.0.2, …) and **features and enhancements** are minor releases (2.1.0, 2.2.0, …).
- Releases are published by GitHub Actions from a `vX.Y.Z` tag, as in 1.x.

## Names

| Thing | Name |
| --- | --- |
| Product | PBC Manager |
| Repository | `bradyloveland/pbcmanager` |
| Server program and service | `pbcm`, `pbcm.service` |
| Program on clients | `pbcm-runner` |
| Service account on clients | `pbcm` |
| Folders | `/opt/pbcm`, `/etc/pbcm`, `/var/lib/pbcm` (same layout on server and clients) |

## Supported clients

Clients must be **Debian 12 (bookworm) or 13 (trixie), or a system based on them**, on x86-64 (the only platform `proxmox-backup-client` is made for), with systemd. End-of-life releases aren't supported.

| Client | How `proxmox-backup-client` is installed |
| --- | --- |
| Debian 12 and 13, and systems that identify as Debian (Proxmox VE 8/9, OpenMediaVault 7/8) | Proxmox's `pbs-client` repository for that release, package `proxmox-backup-client` |
| Other systems based on Debian 12 or 13 (for example Ubuntu 22.04 and 24.04) | The same repository, package `proxmox-backup-client-static`, which has no library dependencies to clash with |
| Anything else (Debian 11 and older, non-Debian systems, ARM) | Refused before anything is installed, with a message saying what's supported |

Setup reads the Debian base from `/etc/debian_version` (`12.x`, `13.x`, or `bookworm/sid` and `trixie/sid` on derivatives). If a client already has the software, the server records its version and leaves it alone.

The Proxmox signing keyring (one keyring covers both releases) is downloaded over HTTPS and also checked against a SHA-256 checksum built into the server, so a tampered key is refused. The source goes in `/etc/apt/sources.list.d/pbcm-pbs-client.list` with `signed-by`.

## How it fits together

```
                 ┌─────────────────────── server (LXC) ───────────────────────┐
Browser ─HTTPS─> │ pbcm: web UI + API, SQLite, SSH key, updater, notifier    │
                 └───────┬──────────────────────────────────────┬─────────────┘
                         │ SSH as pbcm (set up, start,         │ PBS API (server's own
                         │ cancel, collect status and logs)     │ proxmox-backup-client):
                         ▼                                      │ space, snapshot lists
   ┌──────────────────── client ─────────────────────┐          │
   │ systemd timer ─> pbcm-runner run <job>          │          ▼
   │   └─> proxmox-backup-client backup ─────────────┼──────> PBS destination(s)
   │ /etc/pbcm: jobs, credentials                   │
   │ /var/lib/pbcm: run results and logs            │
   └──────────────────────────────────────────────────┘
```

**Backups never pass through the server.** Each client sends data straight to PBS. The server touches PBS only to read datastore space and snapshot lists.

### What happens when something is down

| Situation | What happens |
| --- | --- |
| **Server is down or unreachable** | Scheduled backups run on time. Running backups finish. Results and logs are kept on the client. When the server is back, it collects everything it missed and sends any alerts, marked as reported late. You can't use the web UI until it's back. |
| **Server is restarted or updated** | Same as above: no backup is interrupted. |
| **Client is offline** | The server shows it as offline, and alerts you if it misses a scheduled backup or stays unreachable (time limit set in Settings). Changes you make to its jobs are saved as "waiting to apply" and sent when it's back. |
| **PBS destination is down** | The run fails on the client with the client's error message, as in 1.x. The server reports it when it collects the result. |
| **Server deleted for good** | Clients keep backing up on their last settings forever. The docs explain how to remove everything from a client by hand (one command: `pbcm-runner uninstall`). |

## Clients

### Adding a client

1. In the UI: enter the address, SSH port, and **root** (or a user with sudo) with a password. You can instead paste the server's public key onto the client yourself first.
2. The server shows the client's SSH host key fingerprint. You confirm it, and it's pinned from then on.
3. Over that one root connection, the server:
   1. detects the OS, CPU type and systemd version
   2. installs `proxmox-backup-client` if it's missing ([Supported clients](#supported-clients)), streaming the output into the UI
   3. creates the `pbcm` system user, with no password and a locked password login
   4. adds the server's public key to that user's `authorized_keys`, with `restrict` and a forced command: `command="sudo -n /usr/local/lib/pbcm/pbcm-runner ssh"`. Whatever the server asks for arrives in `SSH_ORIGINAL_COMMAND`, which pbcm-runner parses itself (no shell), so the key can't run anything else even if the server is compromised
   5. installs `pbcm-runner`, plus a sudo rule letting `pbcm` run **only** `pbcm-runner` (and keeping `SSH_ORIGINAL_COMMAND`). The rule is checked with `visudo -c` before it's put in place.
   6. installs the systemd unit templates
4. The server signs in again as `pbcm` to prove the new account works, then **forgets the root password**.

The setup files (the script, pbcm-runner and the server's key) are uploaded into a private temporary folder and the script runs from there. With a sudo user, the password goes only to `sudo -S` on standard input, never on a command line or to any other command. Root is never used again unless you choose **Repair client**, which asks for root again.

The UI suggests turning off root password login over SSH once a client is added. The server doesn't change the client's SSH settings itself.

### What gets installed on a client

| Item | Purpose |
| --- | --- |
| `/usr/local/lib/pbcm/pbcm-runner` | Small Go program, owned by root, signed like server releases. It isn't a background service: systemd starts it for a backup, or the server starts it over SSH for one command, and it exits when done. |
| `/etc/sudoers.d/pbcm` | `pbcm ALL=(root) NOPASSWD: /usr/local/lib/pbcm/pbcm-runner` |
| `/etc/pbcm/client/bundle.json` | Every job and destination the server sent, without secrets, plus the bundle's hash (so the server knows what the client has) |
| `/etc/pbcm/client/credentials/` | Token secrets and key file passwords (see [Credentials on clients](#credentials-on-clients)) |
| `/etc/systemd/system/pbcm-job@.service` | One template unit used by every job |
| `/etc/systemd/system/pbcm-job-<job>.timer` | One timer per scheduled job |
| `/var/lib/pbcm/client/runs/` | One folder per run with its result and log. Kept for 90 days or the last 500 runs. Both limits can be changed in Settings. |

`pbcm-runner` commands (the server calls these through `sudo`):

| Command | Does |
| --- | --- |
| `apply` | Reads the bundle (jobs, destinations, credentials) from its input, writes it, creates/removes timers, reloads systemd. The SSH connection authenticates the server; the bundle isn't separately signed. |
| `run <job>` | What the systemd unit runs: backs up to each destination in turn, writing the result and log |
| `start <job>` / `cancel <job>` | `systemctl start` / stop with SIGINT, then SIGKILL after 20 s |
| `status --since <cursor>` | Results finished since the server's last check, plus anything running now |
| `log <run> --offset <n>` | Log text from an offset, for the live log view |
| `browse <path>`, `du <path>` | Folder picker and folder sizes (`du` at idle priority, with a fallback, as in 1.x) |
| `detect`, `install-client` | OS detection and installing `proxmox-backup-client` |
| `self-update` | Replaces itself with a new signed version read from its input |
| `uninstall` | Removes timers, units, files, the sudo rule and the `pbcm` user. With `--keep-history` it keeps `/var/lib/pbcm`. |

Every command checks its arguments and works only on `pbcm` files and units. Root through the sudo rule can't be used for anything else.

### Credentials on clients

Clients must hold their PBS credentials, because they back up without the server. To keep that safe:

- **Encrypted on disk:**
  - On systemd 250+ (Debian 12 and 13, Ubuntu 24.04), credentials are stored with `systemd-creds encrypt`. That ties them to the machine (and its TPM if it has one), so copying the file to another machine doesn't reveal them.
  - pbcm-runner decrypts them with `systemd-creds decrypt` into `/run/pbcm/<run>/` (memory only) for the length of a run, and deletes them afterwards.
  - Older systemd (Ubuntu 22.04 has 249) uses a root-only file loaded with `LoadCredential=`.
- **Never on a command line or in an environment listing.** `pbcm-runner` points `proxmox-backup-client` at the credential file with `PBS_PASSWORD_FILE` and `PBS_ENCRYPTION_PASSWORD_FILE`, which Proxmox documents.
- **One PBS token per client is recommended.** The token has only the `DatastoreBackup` role, on that client's own **namespace**. A compromised client can then only add backups to its own namespace. It can't read or delete anyone else's backups. The destination form explains how to set this up in PBS.
  - A destination has default credentials and an optional namespace.
  - A client can override both for that destination.
  - Namespaces are passed to the client as `PBS_NAMESPACE`.

### Run history and the server catching up

- Each run writes `result.json` (status, times, exit code, error summary, destination) and `log` in its own folder on the client.
- The server polls each client: every 30 seconds while a run is active, every 5 minutes otherwise (both set in Settings). It copies new results and logs into its database. The live log view reads from the client directly while you watch.
- Run ids are created on the client (time-based and unique), so no run is lost or counted twice however long the server was away.

## Jobs and schedules

- **Schedules** are the 1.x types (by hand, certain days, every few hours), turned into systemd `OnCalendar=` timers.
  - `Persistent=false`, so missed runs are skipped as in 1.x.
  - systemd won't start a job that's already running, so a job never overlaps itself.
- **Several destinations per job:** `pbcm-runner run` backs up to each destination in turn, and records a result per destination. The dashboard shows each one. The job form points out that each extra destination reads every file again, and that a **PBS sync job** (one PBS pulling from another) is usually lighter for an offsite copy.
- **Backups at once per client:** a setting, enforced on the client with a systemd slice. That way it still holds when the server is down.
- **Run now** calls `pbcm-runner start` over SSH. **Cancel** calls `cancel`.

## Server

- **One executable**, with the web UI built in (`go:embed`). It's pure Go, so it builds for x86-64 and ARM64 with no C compiler. The server can run on ARM even though clients can't. `pbcm-runner` builds from the same code.
- **Dependencies kept small:** `golang.org/x/crypto/ssh` and a pure-Go SQLite driver (`modernc.org/sqlite`). Anything else needs a reason.
- **Web UI:** plain JavaScript with no framework and no CDNs, so it works offline under a strict CSP. It's split into a few files, all embedded in the executable.
- **Runs as an unprivileged user,** `pbcm`. It never reads backup data. Backing up the server's own machine works like any other client, over SSH to `127.0.0.1`.
- **The server's own `proxmox-backup-client`** is used only to read datastore space and snapshot lists.

### Data model

| Table | Holds |
| --- | --- |
| `clients` | Name, address, SSH port, pinned host key, OS, CPU type, systemd and client versions, runner version, last contact, pending changes |
| `destinations` | Host, port, datastore, default namespace, user, token name, token secret (encrypted), fingerprint |
| `client_destinations` | Optional credential and namespace overrides per client and destination |
| `jobs` | Client, folders (path and archive name), exclusions, schedule, change detection, speed limit, key file path and password (encrypted), enabled, destinations, backup ID (defaults to the client's hostname), applied version |
| `runs` | Copied from clients: run id, job, destination, status, times, exit code, summary, trigger, email error, reported-late flag |
| `run_logs` | Log text, or a file under the data folder for large logs |
| `settings` | Key/value pairs for everything on the Settings page |
| `auth`, `sessions` | Admin account, TOTP, recovery codes. Sessions survive a restart, so an update doesn't sign you out. |

Schema changes run as numbered migrations at startup.

Secrets in the database are encrypted with a key kept in a separate file (`/etc/pbcm/secret.key`). A copied database file alone doesn't reveal them.

### Email alerts

- Sent by the server when it collects a result: failure, success (optional), or interrupted.
- New alerts:
  - **missed backup**: a scheduled time passed with no run reported
  - **client unreachable** for longer than a set time
  - **destination nearly full**: 80% / 90%, using the same thresholds as the dashboard
- If the server was down, the alert says the result is reported late and when the backup actually ran.

## Self-update

The repository is public, so the server can check GitHub's releases without a token. GitHub allows 60 unauthenticated requests an hour, far more than a daily check needs.

### Getting a release

- **Check for updates:** the Settings page shows the running version and the latest release, with its changelog. A daily automatic check can show a banner when a new version is out.
- **Automatic install** is optional. It's off by default, and when on, it installs only during a time window you choose.
- **Upload:** you can upload a release `.tar.gz` downloaded from GitHub, for servers with no internet access.

### Checking a release

- Each release includes `SHA256SUMS` and an ed25519 signature of it. GitHub Actions signs them with a private key kept in a repository secret.
- The public key is built into the executables. A file whose signature doesn't match is refused, whether downloaded or uploaded.
- Installing an older version is allowed only as an explicit "Roll back to X" action.

### Installing a release on the server

1. Back up the database to `backups/pbcm-<old version>-<time>.db`.
2. Write the new executable next to the old one, keep the old one as `pbcm.prev`, and swap them in one step (rename).
3. Exit. systemd restarts the service, and the new version applies any database migrations. Backups on clients aren't affected.
4. If the new version doesn't report healthy within 60 seconds, or fails to start three times, a small `pbcm-rollback` unit (started by `OnFailure=`) puts back the previous version and database. The UI then says the update was rolled back, and why.

The service user owns the install folder, so this needs no root access.

### Updating clients

After the server updates, it sends the matching `pbcm-runner` to each client with `self-update`. The runner checks the signature itself before replacing anything. The UI lists each client's runner version.

The server keeps working with runners one minor version behind, so an offline client is fine until it's back.

## Settings in the browser

Every setting that was CLI-only or config-file-only in 1.x is on a **Settings** page, plus the new ones:

| Setting | 1.x location | Notes |
| --- | --- | --- |
| Listen address and port | `pbs-manager configure` | See "Network changes" below |
| HTTPS certificate | `configure --tls-cert/--tls-key` | Upload your own cert and key, regenerate the self-signed one, or turn HTTPS off |
| Trusted reverse proxies | `configure --trusted-proxies` | |
| Base path (sub-path hosting) | `configure --base-path` | |
| Backups at once | `configure --max-concurrent` | Now per client, enforced on the client |
| Run history kept | `config.json` only | On the server and on clients |
| Size and status check intervals | Hard-coded | Folder size, snapshot, destination space, client polling |
| Alerts | Partly in Email alerts | Missed-backup and unreachable-client limits, destination-full thresholds |
| Updates | (new) | Automatic check, automatic install and time window |
| SSH | (new) | Server public key, regenerate key (updates every client), connection timeout |

**Network changes.** A wrong port, address or certificate can lock you out, so these work like a monitor resolution change:
1. The server starts listening with the new settings.
2. The UI sends you to the new address with a confirmation prompt.
3. If you don't confirm within about two minutes, the server goes back to the old settings.

**What still needs a terminal:**
- The first install: one command in the LXC.
- Recovery commands for when you're locked out of the web UI: `pbcm passwd`, `pbcm totp-reset`, and `pbcm network --reset` (every interface, port 8099, self-signed HTTPS).
- `pbcm-runner uninstall` on a client whose server is gone.

## Install

- **One command** inside a fresh Debian 12/13 LXC downloads the latest release, checks its signature, and installs. It creates the `pbcm` user, the systemd units and the server's own `proxmox-backup-client`, then prints the URL and a **one-time setup code**.
- **First visit:** the browser asks for the setup code, then an admin username and password. The setup code stops anyone else on the network from claiming a fresh install first.
- **Installer options:** port, `--behind-proxy`, `--proxy-ip` and `--base-path` keep working for scripted installs. All of them can be changed later in the UI.
- The docs will include an LXC recipe: an unprivileged container with a small disk. It needs no access to backup data.

## Moving from 1.x

- **Import a 1.2.0 settings export** (Account, Export settings). The importer asks which client the 1.x jobs belong to, for example the OMV box, and adds it as a client if needed. Each job keeps its single destination. The export has no credentials, so they're entered afterwards.
- **Also accept the 1.x `config.json` itself,** which contains the secrets, so nothing needs re-entering. The UI warns that the file holds credentials, and the server doesn't keep the uploaded file.
- 1.x run history isn't carried over.
- Once the new server's jobs are running, the docs walk through stopping and uninstalling 1.x on the OMV box. Both can run side by side while you compare.

## Feature parity

Everything in 1.2.0, now per client:
- **Destinations:** connection test, datastore space with warnings at 80% and 90% used, fingerprint, token or password sign-in.
- **Jobs:**
  - multiple folders, with archive names
  - exclusions, change detection mode, speed limit, encryption key file and password
  - backup ID
  - schedules (by hand, certain days, every few hours); missed runs are skipped and a job never runs twice at once
  - pause/resume
- **Running:** Run now, cancel, live log, error summary, last 20 runs strip, Activity page, snapshot list.
- **Sizes:** folder sizes, latest backup size, Data protected and Destination space widgets.
- **Email:** alerts on failure, success, or interruption; test email; STARTTLS, SSL or no encryption.
- **Account:**
  - password and two-step sign-in: TOTP with QR enrollment, recovery codes, replay protection
  - username change
  - sign-in throttling per address and for the whole account
- **Network:** HTTPS with a self-signed certificate, reverse proxy headers, base path, `/api/health`.
- **Settings:** export and import, with no credentials in exports.
- **Security:** the 1.x rules carry over:
  - secrets are write-only and never on a command line
  - the `X-PBSM` header (renamed) plus SameSite cookies stop cross-site requests
  - strict CSP, all user content escaped
  - plain, specific messages for users

## Security notes

- **The server is still a high-value machine.** It holds every destination's credentials and can push job settings to every client. Someone who takes over the server could add their own PBS destination to a job and copy a client's data out.
  - Protect the server: VPN only, two-step sign-in, updates kept current.
  - A later 2.x option could let a client accept only destinations approved during root setup.
- **Server compromise doesn't give a shell on clients.** The `pbcm` account can only run `pbcm-runner`, and the runner only does the commands listed above.
- **PBS tokens with only `DatastoreBackup` on a per-client namespace can't delete backups.** Even a compromised client or server can't erase existing backup history.
- **Releases and runner updates are signed,** and checked on both server and client.

## Testing

- **Unit tests:** `go test ./...` covers validation, schedule-to-`OnCalendar` conversion, command building, export/import, auth and TOTP (with the RFC 6238 test values), and QR codes, ported from the 1.x tests.
- **Runner tests** use a fake `proxmox-backup-client` (fails on `fail`, runs slowly on `slow`, as in 1.x) and a fake `systemctl`, run in a temporary folder.
- **SSH tests** use an SSH server started inside the test process. They cover adding a client, host key pinning, the restricted account, apply, start, cancel, status catch-up after the server was "down", and `self-update`.
- **Container test in CI:**
  - start Debian 12 and 13 containers with SSH and systemd
  - add each as a client using a root password
  - check that the real `proxmox-backup-client` gets installed, that the `pbcm` account and sudo rule work, and that a timer fires on schedule with the server stopped
  - no PBS server is needed
- **Updater tests:** build two signed versions with a test key, update from one to the other, and check rollback with a deliberately broken build.
- **Browser smoke test:** Playwright, kept optional.
- **Checks:** `gofmt`, `go vet`, `staticcheck`, and `shellcheck` for the installer. These replace `ruff`.

## Milestones

| Milestone | Delivers |
| --- | --- |
| **M1: base** | Go project, SQLite and migrations, auth (password, TOTP, recovery codes, throttling), setup in the browser, Settings page, embedded UI shell, CI, installer |
| **M2: clients** | SSH key, adding a client as root, host key pinning, OS detection, installing `proxmox-backup-client`, `pbcm` account and sudo rule, `pbcm-runner` install, folder browser, Repair and Remove client |
| **M3: backups** | Destinations and per-client overrides, jobs with several destinations, `apply`, timers, `run`/`start`/`cancel`, credentials on clients, status catch-up, live logs, snapshots, email alerts including missed and unreachable |
| **M4: dashboard** | Results across clients, size widgets, Activity page, banners |
| **M5: updates** | Signed releases, update check, upload, swap, health check, rollback, runner updates |
| **M6: moving from 1.x and release** | 1.x import, docs, LXC recipe, testing on your machines → **2.0.0** |

Each milestone ends with passing tests and a pull request into `v2`, so you can try it in a test LXC as it grows.

## Open questions

1. **Short names:** are `pbcm`, `pbcm-runner` and the `/opt/pbcm` folders OK?
2. **Run history on clients:** are 90 days / 500 runs good defaults?
3. **Polling:** every 30 s during a run and every 5 min otherwise. OK?
