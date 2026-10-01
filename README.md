# PBS Backup Manager

A small web UI for file-level backups with `proxmox-backup-client`. Use it to manage Proxmox Backup Server destinations and credentials, choose which folders to back up, schedule jobs, read live logs, and get an email when a backup fails.

Sign-in uses a username and password, with optional two-step verification (TOTP) through any authenticator app. It can run on its own over HTTPS or behind a reverse proxy.

It's two small Python files plus one HTML page. It uses only the Python standard library, so there's nothing to `pip install`. It runs on any Linux system with Python 3.8+ and systemd.

## Install

Copy the folder to the machine (your OMV box, for example), then:

```bash
cd pbs-manager
sudo ./install.sh
```

The installer:

1. Installs `proxmox-backup-client` from the Proxmox repository if it's missing. This works on Debian 12 (bookworm, OMV 7) and Debian 13 (trixie, OMV 8). On other distributions, install the client yourself.
2. Copies the app to `/opt/pbs-manager` and adds a `pbs-manager` command.
3. Creates a self-signed certificate so the UI is served over HTTPS.
4. Asks you for an admin password.
5. Starts the `pbs-manager` systemd service.

Then open `https://<machine-ip>:8099` and sign in as `admin`.

Installer options:

| Option | What it does |
| --- | --- |
| `--port 9000` | Use another port (default 8099) |
| `--no-tls` | Serve plain HTTP |
| `--behind-proxy` | Reverse proxy on the same machine: listen on 127.0.0.1 only, plain HTTP, trust proxy headers from localhost |
| `--proxy-ip 192.168.1.10` | Reverse proxy on another machine or container: listen on all interfaces, plain HTTP, trust proxy headers from that address (comma-separate several) |
| `--base-path /backups` | Serve under a sub-path, like `https://example.com/backups/` |
| `--skip-client` | Don't install proxmox-backup-client |

### Upgrading

Copy the new files over and run `sudo ./install.sh` again. Your destinations, jobs, history, password and settings are kept, and only the options you pass are changed. For example, to switch an existing install to run behind a proxy, run `sudo ./install.sh --behind-proxy`.

## First-time setup in the UI

1. **Destinations → Add a destination.** Enter your PBS host, datastore, user (e.g. `omv-backup@pbs`), API token name (e.g. `omv-data`), token secret, and the server fingerprint from the PBS dashboard. Click **Test connection** to confirm it works and see datastore usage.
2. **Backup jobs → Create a backup job.** Add folders with **Browse** (OMV drives are under `/srv/dev-disk-by-uuid-…`), pick a schedule, and optionally set exclusions, an upload speed limit, or an encryption key.
3. **Email alerts.** Enter your SMTP details, click **Send a test email**, then save.
4. **Account → Set up two-step verification.** Scan the QR code with your authenticator app, enter the code it shows, and save the recovery codes somewhere safe.
5. **Overview.** Press **Run now** on a job to start the first backup, and click it in the history strip to watch the log.

### Tip for a very large first backup

PBS can't resume an interrupted backup. To build up a big initial backup safely, start the job with one folder, run it, then add the next folder and run it again. Folders already backed up are recognized from the previous snapshot and go by quickly. Keep the archive names and backup ID the same between runs.

## Signing in

There's one admin account. The username starts as `admin`; change it under **Account**, along with your password.

**Two-step verification.** Once it's on, signing in takes your password plus a 6-digit code from an authenticator app (Aegis, 2FAS, Google Authenticator, Microsoft Authenticator, 1Password, Bitwarden and others all work). Setup shows a QR code generated on the server, so nothing leaves your network. Each code works only once, and codes from the previous or next 30-second window are accepted to allow for small clock differences.

**Recovery codes.** You get 10 single-use recovery codes when you turn on two-step verification. Use one in place of a code if you lose your phone. You can create a new set from **Account**, which cancels the old ones.

**Locked out?** On the server:

```bash
sudo pbs-manager passwd      # set a new password
sudo pbs-manager totp-reset  # turn off two-step verification
sudo systemctl restart pbs-manager
```

**Brute-force protection.** After 5 wrong attempts from one address, that address waits a minute. After 20 wrong attempts in total, all sign-ins pause for 5 minutes. Failed attempts are logged with the client's address (`journalctl -u pbs-manager`), which works with fail2ban.

## Running behind a reverse proxy

Install or upgrade with `--behind-proxy` (proxy on the same machine) or `--proxy-ip <proxy address>` (proxy elsewhere). The manager then:

- Uses the real client address from `X-Forwarded-For` for logging and sign-in throttling. It only trusts these headers from the proxy addresses you configured, so they can't be spoofed by other clients.
- Marks the session cookie `Secure` when the proxy reports `X-Forwarded-Proto: https`.
- Answers health checks at `/api/health` without signing in.
- Works at the root of a domain or under a sub-path. All URLs in the page are relative, so the proxy can strip the prefix or pass it through; set `--base-path` if it passes it through.

Let the proxy handle HTTPS. Long-running pages poll every few seconds, so no WebSocket support is needed.

**Nginx**, on its own subdomain:

```nginx
server {
    listen 443 ssl http2;
    server_name backups.example.com;
    # ssl_certificate / ssl_certificate_key ...

    location / {
        proxy_pass http://127.0.0.1:8099;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

**Nginx**, under a sub-path (install with `--base-path /backups`):

```nginx
location /backups/ {
    proxy_pass http://127.0.0.1:8099;   # no trailing slash: passes /backups/ through
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

**Caddy:**

```caddy
backups.example.com {
    reverse_proxy 127.0.0.1:8099
}
```

Caddy sets the forwarding headers and gets a certificate automatically.

**Nginx Proxy Manager:** add a proxy host with scheme `http`, the OMV machine's IP, and port 8099, then request an SSL certificate on the SSL tab. Install the manager with `--proxy-ip <NPM's IP>` so it accepts NPM's forwarded headers. If NPM runs in Docker on the same machine, use the Docker network gateway address (often `172.17.0.1`), or `--proxy-ip 172.16.0.0/12` to cover Docker's default networks.

**Traefik** (labels or file provider): route your host to `http://<omv-ip>:8099` and install with `--proxy-ip <Traefik's IP>`. Traefik sends `X-Forwarded-*` headers by default.

To change proxy settings later without reinstalling:

```bash
sudo pbs-manager configure --trusted-proxies "127.0.0.1,::1,192.168.1.10" --base-path /backups --bind 127.0.0.1
sudo systemctl restart pbs-manager
```

## Where things live

| Path | What |
| --- | --- |
| `/opt/pbs-manager/` | Application (`app.py`, `qr.py`, `static/`) |
| `/etc/pbs-manager/config.json` | Settings, destinations, credentials and the two-step secret (root-only, mode 600) |
| `/etc/pbs-manager/tls.crt`, `tls.key` | Self-signed certificate |
| `/var/lib/pbs-manager/runs.json` | Run history (last 500 runs) |
| `/var/lib/pbs-manager/logs/` | One log file per run |

## Commands

```bash
sudo pbs-manager passwd                     # reset the admin password
sudo pbs-manager totp-reset                 # turn off two-step verification
sudo pbs-manager configure --port 9000      # change port (then restart)
sudo pbs-manager configure --bind 127.0.0.1 # only listen locally, e.g. behind a reverse proxy
sudo pbs-manager configure --max-concurrent 2  # allow two jobs to run at once (default 1)
sudo systemctl restart pbs-manager
journalctl -u pbs-manager -f                # service log
```

To use your own certificate, run `sudo pbs-manager configure --tls-cert /path/cert.pem --tls-key /path/key.pem` and restart.

## Behavior notes

- **One run per job at a time.** If a scheduled run comes up while the previous one is still going, it's skipped rather than stacked. By default only one job runs at a time across the whole machine, so backups don't compete for bandwidth. Others wait in the queue.
- **Missed schedules aren't caught up.** If the machine is off at 3:00, that run is skipped, and the next scheduled time applies.
- **Restarts.** If the service stops mid-backup, that run is marked failed on startup and a failure alert is sent.
- **Pre-flight checks.** Before starting, a run checks that each folder exists (so an unmounted drive fails loudly instead of backing up an empty mount point) and that any key file is present.
- **Retention isn't handled here.** Set up a prune job, garbage collection and verification on the PBS server under Datastore → Prune & GC.
- **Security.** Secrets are stored in a root-only file and never sent back to the browser. Recovery codes are stored only as hashes. Sessions use HttpOnly, SameSite=Strict cookies and expire after 12 hours idle. Changing your password or turning on two-step verification signs out every other session. Even with two-step verification and a proxy, it's safest to reach this over a VPN rather than exposing it to the internet, since it holds the credentials to your backup server.

## Uninstall

```bash
sudo ./uninstall.sh          # keeps settings and history
sudo ./uninstall.sh --purge  # removes everything
```

Nothing on your Proxmox Backup Server is changed either way.
