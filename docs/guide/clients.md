# Clients

A **client** is a Linux machine whose folders you back up. The server sets it up over SSH once; after that the client runs its own backup schedules and sends backups straight to PBS.

## What a client needs

- **Debian 12 or 13, or a system based on them.** That includes Proxmox VE 8/9, OpenMediaVault 7/8 and Ubuntu 22.04/24.04. Proxmox only publishes `proxmox-backup-client` for x86-64, so clients must be x86-64.
- **SSH reachable from the server,** on your LAN or over a VPN.
- **A sign-in for setup:** root, or a user that can use `sudo`. The password is used once and never saved.

## Adding a client

**Clients → Add a client**:

1. Enter its address and SSH port.
2. **Check the host key fingerprint.** Compare it with the client's own, from `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the client. The server pins this key and refuses to connect if it ever changes.
3. Sign in as root or a sudo user. Setup shows its progress.

Setup does the following on the client:
- installs `proxmox-backup-client` from Proxmox if it's missing: the regular package on Debian, the static build on systems based on Debian
- installs `/usr/local/lib/pbcm/pbcm-runner`
- creates a `pbcm` account that signs in only with the server's key and can only run `pbcm-runner`, through a forced command and a sudo rule

The server never signs in as root again.

## While the server is down

Each client has its own copy of its jobs, as systemd timers, and the credentials it needs, encrypted with `systemd-creds` where available. Backups keep running on schedule. When the server is back, it collects the results it missed, including their logs, and reports failures.

## Repair

**Repair** runs setup again. Use it when:
- a client was reinstalled, so its host key changed. Compare the new key first.
- `proxmox-backup-client` or the `pbcm` account was removed
- the Updates page says a client's `pbcm-runner` needs Repair

## Removing a client

**Remove** takes away everything setup added: timers, settings, credentials, `pbcm-runner`, the sudo rule and the `pbcm` account. Backups already on PBS are kept. If the client can't be reached, you can take it off the list instead, and later run `sudo /usr/local/lib/pbcm/pbcm-runner uninstall` on it.

## Folder sizes

Clients measure each backed-up folder in the background at idle priority, every 12 hours by default. The Dashboard adds them up as **Data protected**. Change how often under **Settings → Sizes and space**, or press **Measure again**.
