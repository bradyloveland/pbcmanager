# Preparing Proxmox Backup Server

Backup Manager signs in to PBS with a dedicated user and API token. Giving it its own token means you can see exactly what it does and revoke it without affecting anything else.

## 1. Create a datastore

If you don't have one already: **Administration → Storage / Disks**, or **Datastore → Add Datastore**. Note its name, for example `backup-pool`.

## 2. Create a user

**Configuration → Access Control → User Management → Add**

- User name: `omv` (anything you like)
- Realm: **Proxmox Backup authentication server** (`pbs`)
- Set a password; it isn't used by Backup Manager but PBS requires one.

The full user ID is `omv@pbs`.

## 3. Create an API token

**Access Control → API Token → Add**

- User: `omv@pbs`
- Token name: `omv`
- Leave **Privilege Separation** on.

Copy the **secret** now. PBS shows it only once. If you lose it, select the token and click **Regenerate Secret**.

## 4. Grant permissions to both the user and the token

This is the step that's easiest to miss. With privilege separation on, **a token can never do more than its user**, so both need the role.

**Access Control → Permissions → Add**, twice:

| Path | User / API Token | Role | Propagate |
| --- | --- | --- | --- |
| `/datastore/backup-pool` | `omv@pbs` (User Permission) | DatastoreBackup | Yes |
| `/datastore/backup-pool` | `omv@pbs!omv` (API Token Permission) | DatastoreBackup | Yes |

`DatastoreBackup` lets the token create backups, list and restore its own backups, and read datastore usage for the dashboard. It can't delete backups or change the datastore, so a compromised NAS can't wipe your backup history. Pruning and garbage collection run on the PBS server itself.

If you want Backup Manager to be able to see backups made by other clients too, use `DatastoreReader` in addition, but it isn't needed.

## 5. Copy the fingerprint

**Dashboard → Show Fingerprint**. You need all 32 colon-separated pairs. It's required when PBS uses its default self-signed certificate; without it the client refuses to connect.

## 6. Add the destination

In Backup Manager, **Destinations → Add a destination**:

| Field | Example |
| --- | --- |
| Host or IP address | `192.0.2.10` |
| Port | `8007` |
| Datastore | `backup-pool` |
| Fingerprint | `59:29:b9:…` |
| User | `omv@pbs` |
| API token name | `omv` |
| Token secret | the secret from step 3 |

Click **Test connection**. A usage bar means everything works.

## Set up retention on PBS

Backup Manager doesn't delete old snapshots. On PBS, under **Datastore → (your datastore) → Prune & GC**, add:

- A **prune job**, for example keep 7 daily, 4 weekly and 6 monthly. Retention applies to each backup group separately.
- A **garbage collection** schedule, which actually frees the space pruned snapshots used.
- A **verify job**, so you find out about damaged chunks before you need a restore.
