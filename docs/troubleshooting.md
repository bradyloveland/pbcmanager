# Troubleshooting

Start with the run's log (click it in the history strip or on the Activity page) and the service log:

```bash
journalctl -u pbs-manager -n 100
```

## Test connection or backups fail

| Message | Cause and fix |
| --- | --- |
| `permission check failed` / `no permissions` | The user or token lacks a role on the datastore. A token can never do more than its user, so **both** `omv@pbs` and `omv@pbs!omv` need `DatastoreBackup` on `/datastore/<name>`. See [PBS setup](pbs-setup.md#4-grant-permissions-to-both-the-user-and-the-token). |
| `authentication failed` | Wrong token secret, wrong token name, or the token expired or was disabled. Regenerate the secret in PBS and paste the new one. |
| Certificate or fingerprint errors | Copy the fingerprint again from **PBS Dashboard → Show Fingerprint**; it must have all 32 pairs. If you've installed a trusted certificate on PBS, clear the fingerprint field. |
| `didn't answer within 45 seconds` | Wrong host or port, a firewall, or PBS is down. From the NAS: `curl -k https://<pbs>:8007`. |
| `has no token secret saved` | The destination was imported or its secret cleared. Edit it and enter the secret. |
| `These folders don't exist or aren't mounted` | The drive isn't mounted or the path changed. Check `ls /srv`. |
| `proxmox-backup-client isn't installed` | Install the `proxmox-backup-client` package or re-run the installer. |

## Backups are slow or re-read everything

- Use **Metadata** change detection.
- Don't change the archive name or backup ID; a new name has nothing to compare against.
- After a failed run, the next run compares against the last *successful* snapshot.
- Running several jobs at once from the same spinning disk is usually slower than one at a time.

## Exclusions don't work

- Patterns are case-sensitive: `/Movies` won't skip `movies`.
- A leading `/` anchors to the top of the folder being backed up.
- Check the command at the top of the run log; each pattern appears as `--exclude <pattern>`.
- Confirm in the PBS web interface's file browser for the snapshot.

## Dashboard sizes

- **"Folders haven't been measured yet"**: measurement starts a few seconds after the service starts and runs one folder at a time. A very large folder with many files can take several minutes. Click **Measure again** to force it.
- **Folder size seems wrong**: it's the apparent size of everything under the folder on the same filesystem, including files you exclude.
- **Latest backup shows an error**: the snapshot list request failed; the message is the same as a Test connection failure.
- **Destination space doesn't update**: it refreshes every 15 minutes; click **Check now**.

## Email alerts don't arrive

- Use **Send a test email** and read the error it shows.
- Gmail and Microsoft 365 need an app password, not your normal password.
- Check your spam folder.
- The Activity page shows "Alert email failed" with the reason if an alert couldn't be sent.

## Can't sign in

```bash
sudo pbs-manager passwd        # new password
sudo pbs-manager totp-reset    # if you've lost your authenticator and recovery codes
sudo systemctl restart pbs-manager
```

"Too many attempts" clears after a minute (or five minutes for the account-wide lockout).

## Behind a proxy: everyone shows as the proxy address

The proxy's address isn't in `trusted_proxies`. Check the start-up log line listing trusted addresses, then:

```bash
sudo pbs-manager configure --trusted-proxies "127.0.0.1,::1,<proxy ip>"
sudo systemctl restart pbs-manager
```

For Docker-based proxies, see [Reverse proxy](reverse-proxy.md#nginx-proxy-manager).

## The page is blank under a sub-path

Make sure the URL ends in a slash (`/backups/`), or set `--base-path /backups` so the app redirects for you.
