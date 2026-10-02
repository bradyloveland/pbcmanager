# Troubleshooting

## Locked out of the web UI

On the server:

| Problem | Command |
| --- | --- |
| Forgot the password | `sudo pbcm passwd` |
| Lost the authenticator and recovery codes | `sudo pbcm totp-reset` |
| Changed the address, port or certificate and can't reach it | `sudo pbcm network --reset` (every interface, port 8099, self-signed HTTPS) |
| An update left the UI unreachable | `sudo systemctl stop pbcm && sudo pbcm rollback && sudo systemctl start pbcm` |

## Logs

- **The server:** `journalctl -u pbcm -n 100`
- **A backup's log:** open the run under **Activity**. Logs are copied to the server when a run finishes.
- **On a client:** `journalctl -u 'pbcm-job@*'` shows its backup runs.

## "Setup finished, but the server couldn't sign in as pbcm"

The client's SSH settings don't let the `pbcm` account in. On the client, run `sshd -T | grep -iE '^(allow|deny)(users|groups)'` to see the limits:
- **OpenMediaVault** (`allowgroups root _ssh`): run `usermod -aG _ssh pbcm`. Setup does this itself from version 2.0.1.
- **`allowusers …`:** add `pbcm` to the `AllowUsers` line in `/etc/ssh/sshd_config`, then run `systemctl reload ssh`.

Then use **Repair**.

## "On ARM64 … Proxmox only builds proxmox-backup-client for Debian 13"

The client is an ARM64 machine, such as a Raspberry Pi, running an OS based on Debian 12. Proxmox only builds `proxmox-backup-client` for ARM64 on Debian 13, so move it to a Debian 13 based OS (Raspberry Pi OS based on Debian 13, or Debian 13 for ARM) and use **Repair**. If `uname -m` says `armv7l`, the OS is 32-bit; install a 64-bit one.

## A client shows "Can't reach"

The server couldn't open SSH to it. Check that the machine is on, that SSH is running, and that the network or VPN between them works. Backups on the client keep running meanwhile, and the results are collected when it's back.

## "The client's SSH host key has changed"

If the client was reinstalled, compare the new fingerprint with the client's own, from `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the client. Then use **Repair**. If nothing changed on the client, treat it as a warning: something else may be answering at that address.

## A backup fails with a permission error

The token needs the **DatastoreBackup** role on the datastore, or on its namespace if the destination uses one. See [Setting up Proxmox Backup Server](pbs.md).

## "Destination space" says it can't check

The server uses its own `proxmox-backup-client` for this. Re-run `install.sh` to install it. If it reports a permission error instead, give the token **DatastoreAudit** on the datastore. Backups aren't affected either way.

## Email alerts don't arrive

Under **Alerts**, press **Send a test email**: it says exactly what the mail server answered. The **Recent alerts** list shows whether each email was sent.
