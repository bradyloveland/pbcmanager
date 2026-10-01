# Using the app

## How backups are organized on PBS

Each **job** has a **backup ID**, and PBS stores its snapshots in the group `host/<backup ID>`. Every run adds one snapshot to that group. Each **folder** in a job becomes an **archive** inside the snapshot (`<archive name>.pxar`).

Recommendations:

- Give each job its own backup ID and keep it unchanged once the job has run. Two jobs sharing an ID would overwrite each other's history and break incremental reuse.
- Keep archive names unchanged. With metadata change detection, unchanged files are recognized from the previous snapshot's archive of the same name and aren't read or sent again.
- Separate jobs for data with different needs (a nightly documents job, a weekly media job) is cleaner than one large job. Deduplication works across all groups in a datastore, so this costs no extra space.

## Destinations

A destination is a PBS server plus one datastore and the credentials to reach it. See [Preparing Proxmox Backup Server](pbs-setup.md).

The token secret is write-only: once saved, it's never shown or sent back to the browser. Leave the field blank when editing to keep it. **Check connection** on the Destinations page runs a live test and refreshes the usage bar.

## Backup jobs

| Setting | Notes |
| --- | --- |
| Folders | Each becomes an archive. **Browse** starts in `/srv`, where OpenMediaVault mounts drives. A backup never crosses into another mounted filesystem inside a folder. |
| Skip these files and folders | One pattern per line, relative to each folder. `/Movies` matches only the top-level `Movies` folder; `Movies` matches a folder of that name at any depth; `**/*.tmp` matches by extension anywhere. Patterns are case-sensitive. |
| Change detection | **Metadata** (default) skips files whose size and modification time are unchanged; by far the fastest for large shares. **Data** reads every file on every run. **Legacy** uses the older single-archive format. |
| Upload speed limit | Per second, such as `20MiB`. Useful for offsite destinations. |
| Encryption key file | Optional client-side encryption. Create a key with `proxmox-backup-client key create /root/pbs.key` and keep a copy somewhere other than this machine; without it the backups can't be restored. |
| Run on schedule | Turn off to pause a job without deleting it. **Run now** still works. |

Before each run, the app checks that every folder exists (so an unmounted drive fails loudly instead of backing up an empty mount point), that the key file exists, and that the destination has a token secret.

## Schedules and the queue

- **By hand only**, **on certain days** at a time, or **every few hours** (1, 2, 3, 4, 6, 8 or 12) at a minute past the hour.
- Times use the machine's time zone.
- By default one job runs at a time. Jobs that come due together wait in a queue and run one after another, in the order they're listed. Raise the limit with `pbs-manager configure --max-concurrent N` if your jobs read from different physical disks.
- A job never runs twice at once. If its next scheduled time arrives while it's still running or waiting, that run is skipped rather than queued again.
- Missed schedules (machine off) aren't made up; the next scheduled time applies.
- Restarting the service clears the queue. A backup interrupted by a restart is marked failed and triggers an alert.

## Seeding a very large folder

PBS can't resume an interrupted backup: if a run fails partway, nothing from that run is kept. To build up a large first backup safely, use exclusions and remove them one run at a time:

1. Create a job for the parent folder, for example `/srv/<disk>/media`, and exclude the big subfolders:
   ```
   /Movies
   /Music
   /TV
   ```
2. Run it. Only the remaining content is backed up.
3. Delete one line, save, run again. Content already backed up is recognized and skipped quickly; only the newly included folder is read and sent.
4. Repeat until no exclusions remain.

The job ends up backing up the whole parent folder, including subfolders you create later, with no cleanup needed. Don't change the folder, archive name or backup ID along the way.

## The dashboard

**Health line.** Green when every job's last finished run succeeded, red when any failed. The Overview tab in the sidebar shows a count of failing jobs.

**Run history strip.** The last 20 runs of each job, oldest on the left. Green succeeded, red failed (and taller), amber cancelled, blue running or waiting. Click a run to open its log.

**Data protected.** Compares two numbers:

- *In your latest backups*: the size PBS reports for each job's newest snapshot, summed across jobs. Refreshed hourly and after every successful run.
- *In your folders*: the size on disk of every folder your jobs back up, measured with `du` at idle priority every 12 hours, when a job is created or changed, or when you click **Measure again**. A folder inside another job's folder is counted once.

While you're seeding with exclusions, the bar shows your progress. Afterwards it stays slightly below 100% if you permanently exclude files, since excluded files count toward folder size but not the backups.

**Destination space.** Used and free space for each datastore, refreshed every 15 minutes or when you click **Check now**. The bar turns amber at 80% used and red at 90%.

## Email alerts

Alerts go out when a backup fails or is interrupted by a restart, and optionally when one succeeds. Each failure alert includes the reason, timing and the last 40 lines of the log. Use **Send a test email** before saving. For Gmail, use `smtp.gmail.com`, port 587, STARTTLS and an app password.

If an alert can't be sent, the run shows the email error on the Activity page and in the run's log view.

## Export and import settings

**Account → Export and import settings**, or `pbs-manager export -o settings.json`.

The export is a JSON file with destinations, jobs, schedules, email settings (without the password), server options and general settings. It never contains:

- destination token secrets
- the email password
- encryption key file passwords
- your username, password, two-step secret or recovery codes

**Import** shows a preview first: how many destinations and jobs, which ones will need credentials, and what would be removed. Confirming replaces the current destinations, jobs and email settings. Run history and your sign-in are kept. Server options in the file (port, TLS, proxy) are not applied, since they're specific to each machine.

Importing onto the same machine keeps every secret already saved for destinations and jobs with the same IDs, so export and import works as a settings backup. Importing onto a new machine leaves secrets blank; the dashboard lists the destinations that need one, and backups won't start until it's entered.

From the command line, stop the service first so it doesn't overwrite the imported file:

```bash
sudo systemctl stop pbs-manager
sudo pbs-manager import settings.json
sudo systemctl start pbs-manager
```

Encryption key files themselves aren't part of the export. Copy them to the new machine separately, to the same path.
