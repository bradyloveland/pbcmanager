# Installation

## Requirements

- Linux with systemd (Debian, Ubuntu, OpenMediaVault, Proxmox VE and similar)
- Python 3.8 or newer (already present on Debian-based systems)
- Root access. The service runs as root because it has to read every folder it backs up.
- `proxmox-backup-client`. The installer adds it automatically on Debian 12 (bookworm, OMV 7) and Debian 13 (trixie, OMV 8). On other distributions, install it yourself.
- Optional: `openssl` for the self-signed HTTPS certificate, and GNU `du` for fast folder measurement (a slower built-in fallback is used otherwise).

## Install

From a release (recommended):

```bash
curl -LO https://github.com/bradyloveland/pbswebclient/releases/download/v1.2.0/pbswebclient-1.2.0.tar.gz
tar xzf pbswebclient-1.2.0.tar.gz
cd pbswebclient-1.2.0
sudo ./install.sh
```

Check the [Releases page](https://github.com/bradyloveland/pbswebclient/releases) for the newest version; each release also has a `.sha256` file to verify the download.

Or from the repository:

```bash
git clone https://github.com/bradyloveland/pbswebclient.git
cd pbswebclient
sudo ./install.sh
```

The installer:

1. Installs `proxmox-backup-client` from the Proxmox repository if it's missing.
2. Copies the app to `/opt/pbs-manager` and adds the `pbs-manager` command.
3. Creates a self-signed certificate so the UI is served over HTTPS.
4. Asks for an admin password (the username is `admin`; you can change it later).
5. Installs and starts the `pbs-manager` systemd service.

Open `https://<machine-ip>:8099`. Your browser will warn about the self-signed certificate the first time.

## Installer options

| Option | What it does |
| --- | --- |
| `--port N` | Port for the web UI (default 8099) |
| `--no-tls` | Serve plain HTTP |
| `--behind-proxy` | Reverse proxy on this machine: listen on 127.0.0.1 only, plain HTTP, trust proxy headers from localhost |
| `--proxy-ip IP[,IP]` | Reverse proxy elsewhere: listen on all interfaces, plain HTTP, trust proxy headers from these addresses or networks |
| `--base-path /path` | Serve under a sub-path such as `https://example.com/backups/` |
| `--skip-client` | Don't install `proxmox-backup-client` |

## Upgrading

Download and extract the new release, or in your clone run `git pull`, then:

```bash
sudo ./install.sh
```

Destinations, jobs, run history, your password, two-step verification and server settings are kept. Only the options you pass are changed, so `sudo ./install.sh --behind-proxy` switches an existing install to proxy mode without touching anything else.

Upgrading restarts the service. A backup running at that moment is marked failed (and an alert is sent); start it again afterwards.

## Changing settings later

```bash
sudo pbs-manager configure --port 9000
sudo pbs-manager configure --bind 127.0.0.1
sudo pbs-manager configure --tls-cert /path/cert.pem --tls-key /path/key.pem
sudo pbs-manager configure --tls-cert "" --tls-key ""          # plain HTTP
sudo pbs-manager configure --trusted-proxies "127.0.0.1,::1"
sudo pbs-manager configure --base-path /backups
sudo pbs-manager configure --max-concurrent 2                    # jobs that may run at once
sudo systemctl restart pbs-manager
```

## Uninstalling

```bash
sudo ./uninstall.sh          # keeps settings and history
sudo ./uninstall.sh --purge  # removes everything, including stored credentials
```

Nothing on your Proxmox Backup Server is changed either way.

## Where things live

| Path | Contents |
| --- | --- |
| `/opt/pbs-manager/` | Application: `app.py`, `qr.py`, `static/index.html` |
| `/usr/local/bin/pbs-manager` | Command-line wrapper |
| `/etc/pbs-manager/config.json` | Settings, destinations, credentials and the two-step secret (mode 600, root only) |
| `/etc/pbs-manager/tls.crt`, `tls.key` | Self-signed certificate |
| `/var/lib/pbs-manager/runs.json` | Run history (last 500 runs by default) |
| `/var/lib/pbs-manager/sizes.json` | Cached folder sizes, backup sizes and destination space |
| `/var/lib/pbs-manager/logs/` | One log file per run |
| `/etc/systemd/system/pbs-manager.service` | Service unit |
