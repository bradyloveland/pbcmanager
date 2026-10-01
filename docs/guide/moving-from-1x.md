# Moving from PBS Backup Manager 1.x

Version 1.x ran on the machine it backed up, such as an OpenMediaVault box. Version 2 runs on a separate server and manages that machine as a **client**.

Your existing backups on PBS stay where they are. The imported jobs keep their **backup ID**, so new backups continue in the same backup group.

## 1. Install the new server

Follow [Installing the server](install.md), then add your destinations' PBS server if you like. The import can also add them for you.

## 2. Add the old machine as a client

**Clients → Add a client**, using the machine 1.x runs on. See [Clients](clients.md). 1.x and the client can run side by side.

## 3. Get the settings out of 1.x

Use either of these:
- **A settings export.** In 1.x, go to **Account → Export settings**. This has no credentials, so you'll enter the PBS token secrets, key file passwords and mail password while importing.
- **The 1.x `config.json` itself,** at `/etc/pbs-manager/config.json` on the old machine. It includes the credentials, so nothing needs typing again. It's only read during the import and isn't kept on the new server. Copy it with care: it holds passwords.

## 4. Import

On the new server, go to **Settings → Export and import → Import a settings file** and choose the file. The preview shows:
- which client the jobs go to (pick the old machine)
- each destination: added, or matched to one you've already added
- each job, and anything it needs: a token secret, or a key file password
- whether to bring over the email alert settings and the run-history limit

**Imported jobs start paused.** Otherwise 1.x and version 2 would both back up the same folders. Leave **Turn the jobs' schedules on now** off.

What doesn't come over:
- **Run history.** It stays in 1.x.
- **The admin account.** You set a new one up on the new server.
- **Network settings.** They belong to the new server.

## 5. Stop 1.x

On the old machine:

```bash
systemctl disable --now pbs-manager
```

1.x runs its schedules inside that service, so this stops its backups.

## 6. Turn on the new jobs

For each imported job, choose **Edit job**, tick **Run on schedule**, and save. The client gets the schedule straight away. To try a job before its first scheduled run, press **Run now**. A paused job can't be run, not even by hand.

## 7. Remove 1.x

Once you're happy, remove 1.x by running its `uninstall.sh`, from the 1.x release you installed from:

```bash
sudo ./uninstall.sh            # keeps /etc/pbs-manager and /var/lib/pbs-manager
sudo ./uninstall.sh --purge    # deletes them too
```

Keep `/etc/pbs-manager/config.json` until the new server's backups have been running for a while. It's your way back.
