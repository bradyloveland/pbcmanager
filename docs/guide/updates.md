# Updates

PBC Manager updates itself from the web UI. Nothing needs a terminal.

## Installing an update

Under **Updates**:
- **Check now** asks GitHub for the newest release and shows its notes. The server also checks once a day, and the Dashboard shows a notice when a new version is out.
- **Download and install** fetches the release, checks it, and installs it.
- **Install from a file** is for a server without internet access. Download `pbcm-<version>-linux-<arch>.tar.gz` from the [releases page](https://github.com/bradyloveland/pbcmanager/releases), then upload it. Use `amd64` for x86-64 and `arm64` for ARM.

The server restarts for a few seconds and the page reloads by itself. Backups on clients aren't affected.

To install new versions automatically, turn on **Settings → Updates → Install updates automatically** and choose the hour.

## What's checked

Every release is signed by the project. Before installing, the server checks the signature and every file's checksum. It refuses anything unsigned, changed, or containing files it doesn't expect. The same check protects uploaded files.

## If an update goes wrong

Before installing, the server backs up its database and keeps the previous version. If the new version stops with an error three times before it has run for 30 seconds, the previous version and its database are put back automatically. The Updates page then says why.

You can also go back by hand with **Go back to *version***. This restores the database as it was just before the update, so changes made since are lost. Clients keep their own run history either way.

If the web UI can't be reached at all after an update, run this on the server:

```bash
sudo systemctl stop pbcm && sudo pbcm rollback && sudo systemctl start pbcm
```

## "Some of this version's files are missing"

Versions before 2.2.1 installed a fixed list of files, so an update to a release with a new file left that file out. Updating from 2.1.0 to 2.2.0, for example, skipped the ARM64 runner (`pbcm-runner-arm64`). The Updates page now checks the program folder against the version's signed file list and names anything missing. **Reinstall this version** downloads the same version from GitHub again, checks it and installs it over itself, keeping every setting. On a server without internet access, run `install.sh` from the release archive instead.

## Clients

After the server updates, it sends the matching `pbcm-runner` to each client at its next check-in, within 5 minutes. The client checks the signature itself before replacing anything. The **Clients** section of the Updates page shows each one's version.

Clients set up before version 2.0.0's release need one **Repair** to get a runner that can update itself.
