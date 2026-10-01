# PBC Manager

A self-hosted web UI for file-level backups with `proxmox-backup-client`. One central server manages backups on many Linux machines over SSH, each sending its data straight to one or more Proxmox Backup Server destinations.

> **Version 2 is in development** on this branch (`v2`). It's a rewrite in Go and isn't ready for use yet. The working single-machine version, 1.2.0, is on [`main`](https://github.com/bradyloveland/pbcmanager/tree/main) and in [Releases](https://github.com/bradyloveland/pbcmanager/releases/tag/v1.2.0).

## How version 2 works

- **Clients never depend on the server.** Each client keeps its own schedule and credentials under systemd and backs up straight to PBS. If the server is down, backups still run, and the server catches up on results when it's back.
- **Set up over SSH.** The server connects to a client as root once, installs `proxmox-backup-client` if it's missing, and creates a limited `pbcm` account for everything after that.
- **Many clients and destinations.** A job can back up to more than one PBS destination.
- **Everything in the browser.** Setup, every setting and updates happen in the web UI. Only the first install, and the recovery commands for when you're locked out, need a terminal.

The full plan is in [docs/design-v2.md](docs/design-v2.md).

## Progress

| Milestone | State |
| --- | --- |
| M1: server base: sign-in with two-step verification, setup in the browser, Settings page (including network and HTTPS with confirm-or-undo), installer, CI | **Done** |
| M2: clients over SSH: add as root once, automatic `proxmox-backup-client` install, limited `pbcm` account, host key pinning, folder browser, repair and remove | **Done** |
| M3: backups | Next |
| M4: dashboard | |
| M5: updates from the browser | |
| M6: moving from 1.x, docs, 2.0.0 release | |

## Trying the development version

The server runs on Debian 12 or 13 (an LXC container works well) on x86-64 or ARM64. Build a package and install it:

```bash
make dist                      # on a machine with Go 1.26+
scp dist/pbcm-*-linux-amd64.tar.gz root@your-lxc:
```

On the server:

```bash
tar xzf pbcm-*-linux-amd64.tar.gz && cd pbcm-*-linux-amd64
sudo ./install.sh
```

The installer prints the address and a one-time **setup code**. Open the address and enter the code to create the admin account. Run `sudo ./install.sh --help` for options such as `--behind-proxy`. Everything they set can also be changed later under Settings.

If you're locked out:

```bash
sudo pbcm passwd            # set a new password
sudo pbcm totp-reset        # turn off two-step verification
sudo pbcm network --reset   # every interface, port 8099, self-signed HTTPS
```

## Development

See [docs/development.md](docs/development.md).

## License

MIT. See [LICENSE](LICENSE).

This project isn't affiliated with or endorsed by Proxmox Server Solutions GmbH. Proxmox is their registered trademark.
