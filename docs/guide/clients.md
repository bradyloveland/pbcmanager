# Clients

A **client** is a Linux machine whose folders you back up. The server sets it up over SSH once; after that the client runs its own backup schedules and sends backups straight to PBS.

## What a client needs

- **Debian 12 or 13, or a system based on them.** That includes Proxmox VE 8/9, OpenMediaVault 7/8 and Ubuntu 22.04/24.04.
- **x86-64, or ARM64 (aarch64) on Debian 13.** Proxmox builds `proxmox-backup-client` for x86-64 on Debian 12 and 13. It builds for ARM64 only on Debian 13, so ARM64 clients need a Debian 13 based OS. See [Raspberry Pi and other ARM64 machines](#raspberry-pi-and-other-arm64-machines).
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

If the client only lets certain groups sign in over SSH, as OpenMediaVault does with `AllowGroups root _ssh`, setup adds `pbcm` to an allowed group. It never chooses one that grants admin rights. If it's limited by user (`AllowUsers`) instead, setup doesn't edit your SSH settings; its log says which line to change.

## While the server is down

Each client has its own copy of its jobs, as systemd timers, and the credentials it needs, encrypted with `systemd-creds` where available. Backups keep running on schedule. When the server is back, it collects the results it missed, including their logs, and reports failures.

## Repair

**Repair** runs setup again. Use it when:
- a client was reinstalled, so its host key changed. Compare the new key first.
- `proxmox-backup-client` or the `pbcm` account was removed
- the Updates page says a client's `pbcm-runner` needs Repair

## Removing a client

**Remove** takes away everything setup added: timers, settings, credentials, `pbcm-runner`, the sudo rule and the `pbcm` account. Backups already on PBS are kept. If the client can't be reached, you can take it off the list instead, and later run `sudo /usr/local/lib/pbcm/pbcm-runner uninstall` on it.

## proxmox-backup-client updates

Each client checks its package lists once a day, without running `apt update`. When a newer `proxmox-backup-client` is listed, the Clients list, the client page and the Updates page say so. To be emailed about it, turn on **When a client's proxmox-backup-client has an update waiting** under **Alerts**.

To install a bug-fix version from here, point at "update to … available" in the Clients list and click **Update now**, or use **Update now** on the client's page or the Updates page. The client then:

- installs only `proxmox-backup-client` (or `-static`), at exactly the version shown, plus anything that version needs. It never removes packages and keeps any changed config files.
- refuses while one of its backups is running. Try again when it has finished.
- waits for another program that's installing updates (such as unattended upgrades) to finish first.
- runs `apt update` and tries again if its package lists are too old to download that version.

A new major version (say 5.x on a client running 4.x) usually comes with an upgrade of the operating system, so there's no button for it: follow Proxmox's upgrade notes on the machine itself. You can still update any client the way you update the rest of that machine: OpenMediaVault's Update Management, Proxmox VE's Updates page, or `apt update && apt upgrade`.

## Raspberry Pi and other ARM64 machines

A Raspberry Pi, or any ARM64 machine, can be a client if:
- **it runs a 64-bit OS.** `uname -m` must say `aarch64`; a 32-bit OS (`armv7l`) won't work.
- **its OS is based on Debian 13**, such as Raspberry Pi OS based on Debian 13, or Debian 13 for ARM. A Debian 12 based OS is refused, because Proxmox doesn't build the client for it on ARM64.

Setup adds Proxmox's Debian 13 `pbs-client` repository with its **`test`** component, which is where Proxmox publishes the ARM64 builds. The package is still checked against Proxmox's signing key. It's Proxmox's testing channel rather than its stable one, so it can be a version or two behind the x86-64 build. Everything else works as on x86-64.

## Backing up the whole system

A job can back up `/`. Each folder's backup stays on that folder's own filesystem, so other filesystems mounted inside it aren't included:
- Virtual ones such as `/proc`, `/sys`, `/dev`, `/run` and a tmpfs `/tmp` are skipped. There's nothing to back up there.
- Real disks are skipped too: the Raspberry Pi's boot partition (`/boot/firmware`), USB drives, and NFS or SMB mounts.

The job form lists the disks inside the folders you picked, with **Add as a folder** to include one as an archive of its own.

When you pick `/`, the form also adds the usual excludes that exist on the client to **Skip these files and folders**:
- swap files, such as `/var/swap`
- downloaded packages (`/var/cache/apt/archives`)
- `/var/tmp` and `/lost+found`

They're ordinary lines you can remove. If the client runs Docker, the form says so: `/var/lib/docker` holds volumes as well as images, so decide whether to keep it.

## Folder sizes

Clients measure each backed-up folder in the background at idle priority, every 12 hours by default. The Dashboard adds them up as **Data protected**. Change how often under **Settings → Sizes and space**, or press **Measure again**.

## Progress of running backups

While a backup runs, the Dashboard, the Backup jobs list, the client's page and the run page show how far it has got, with a percentage and the time left. proxmox-backup-client reports how much it has read once a minute, and the server checks running clients every 30 seconds, so the bar moves in steps.

The total comes from measuring the job's folders when the run starts. That leaves out the job's excludes and other disks, as the backup does, and runs at idle priority alongside the backup without delaying it. Until the measurement finishes, the total is the previous run's size, marked "about". A job's first backup shows the amount read so far until the measurement is done. The bar stops at 99% until the run actually ends, because files can change while it runs.
