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
