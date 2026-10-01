# Setting up Proxmox Backup Server

Each **destination** in PBC Manager is a datastore on a Proxmox Backup Server, plus the credentials clients use to reach it. Give each client its own API token with only the rights it needs.

## Create a user and an API token

In the PBS web UI:

1. **Configuration → Access Control → User Management → Add.** Create a user in the `pbs` realm, for example `nas@pbs`. Its password isn't used afterwards.
2. **API Token → Add.** Choose that user and a token name, for example `pbcm`. Copy the **secret** it shows. PBS shows it only once.
3. **Datastore → *your datastore* → Permissions → Add → API Token Permission**:
   - Path: `/datastore/<datastore>`, or `/datastore/<datastore>/<namespace>` to keep the client in its own namespace
   - API token: `nas@pbs!pbcm`
   - Role: **DatastoreBackup**

`DatastoreBackup` can create backups and read the token's own ones, but not delete or prune them. A compromised client, or server, therefore can't erase your backup history.

If **Destination space** on the Dashboard shows a permission error, also give the token the read-only **DatastoreAudit** role on the same path.

## Get the fingerprint

PBS uses a self-signed certificate unless you've replaced it. Find its fingerprint on the PBS **Dashboard → Show Fingerprint**, or run `proxmox-backup-manager cert info` on the PBS host. Clients check it before trusting the server.

## Add the destination

In PBC Manager, go to **Destinations → Add a destination** and enter:

| Field | Example |
| --- | --- |
| Name | Home PBS |
| Host and port | `192.0.2.10`, `8007` |
| Datastore | `backup-pool` |
| Namespace (optional) | `clients/nas` |
| User | `nas@pbs` |
| Token name | `pbcm`. Leave it blank to sign in with the user's password instead. |
| Token secret | the secret from step 2 |
| Fingerprint | from the PBS dashboard |

Press **Test connection**. It signs in and shows the datastore's free space.

The secret is stored encrypted and never shown again. When editing, leave it blank to keep it.

A job can send each backup to several destinations, such as a local PBS and an offsite one.
