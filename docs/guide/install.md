# Installing the server

PBC Manager's server is one program with its web UI built in. It runs on **Debian 12 or 13**, x86-64 or ARM64. A small LXC container on Proxmox VE is the usual home for it.

The server only manages clients and shows their status. Backups run on the clients themselves, straight to Proxmox Backup Server, so they carry on if the server is down.

## 1. Create a container (Proxmox VE)

In the Proxmox web UI, choose **Create CT**:

| Setting | Value |
| --- | --- |
| Template | Debian 12 or 13 standard (download it under the storage's **CT Templates** first) |
| Unprivileged container | Yes |
| Disk | 4 GB |
| CPU | 1 core |
| Memory | 512 MB |
| Network | A static address, or a DHCP reservation, on a network that can reach your clients and PBS |

Under **Options → Features**, turn on **Nesting**. It lets the service use systemd's sandboxing. Without it the installer still works, but turns the sandboxing off.

Start the container and open its console.

## 2. Run the installer

As root in the container:

```bash
apt-get update && apt-get install -y curl
curl -fsSLO https://raw.githubusercontent.com/bradyloveland/pbcmanager/main/install.sh
bash install.sh
```

The installer downloads the newest release, checks its checksum, and sets everything up:
- a `pbcm` service account
- `/opt/pbcm` for the program, `/etc/pbcm` for settings and `/var/lib/pbcm` for data
- a systemd service
- `proxmox-backup-client`, which the server uses to check datastore space and list snapshots

At the end it prints the web address and a **setup code**.

Options (run `bash install.sh --help` for all of them):

| Option | Use it for |
| --- | --- |
| `--port 443` | A different port (the default is 8099) |
| `--no-tls` | Plain HTTP, when something else handles HTTPS |
| `--behind-proxy` | A reverse proxy on the same machine: listen on 127.0.0.1 with plain HTTP |
| `--proxy-ip 192.0.2.5` | A reverse proxy on another machine: trust its forwarded headers |
| `--base-path /backups` | Serving under a sub-path, such as `https://example.net/backups/` |
| `--version 2.0.0` | A specific release instead of the newest |

Every one of these can be changed later under **Settings** in the web UI. Running the installer again upgrades in place and keeps your settings.

To install from a downloaded release instead, extract `pbcm-<version>-linux-<arch>.tar.gz` and run its `install.sh`. It installs that copy without going to the internet.

## 3. Finish setup in the browser

Open the address the installer printed, such as `https://192.0.2.20:8099/`. The server uses a self-signed certificate at first, so your browser warns you once. You can upload your own certificate under **Settings → Network**.

Enter the setup code, then choose an admin username and password. If you've lost the code, `sudo pbcm setup-code` shows it again, until setup is done.

Then:
1. Turn on **two-step verification** under **Account**. The server holds every destination's credentials, so it deserves it.
2. Add a destination: see [Setting up Proxmox Backup Server](pbs.md).
3. Add a client: see [Clients](clients.md).
4. Create a backup job, choosing its folders, schedule and destinations.
5. Set up **email alerts** under **Alerts**.

## Keep it private

Reach the server on your LAN or over a VPN, never from the public internet. It can sign in to every client and holds every PBS token.
