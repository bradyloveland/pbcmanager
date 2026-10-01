# CLAUDE.md

Context for Claude Code sessions working on this repository.

## What this is

Proxmox Backup Client Web Manager (`pbcwm`), version 2: a Go rewrite of PBS Backup Manager 1.x.
- A central server (usually in a Debian LXC) manages file-level backups on many Linux clients over SSH.
- Clients run their own schedules (systemd timers) and back up straight to Proxmox Backup Server. **Clients must never depend on the server:** if it's down, backups still run.
- The owner's environment is Debian-based: OpenMediaVault, Debian, Proxmox hosts. Clients are reached on the LAN or over a VPN, never the public internet.

The plan is in `docs/design-v2.md`, and progress by milestone is in `README.md`. 1.x (Python) lives on `main` until 2.0.0 ships; `v2` is the development branch.

## Hard rules

- **No terminal needed after install.** Every setting must be visible and changeable in the web UI. The only CLI-only things are the first install and the lock-out recovery commands (`pbcwm passwd`, `pbcwm totp-reset`, `pbcwm network --reset`).
- **Settings changes must keep working for existing installs.** Schema changes are new numbered files in `internal/store/migrations/`, never edits to old ones. The installer upgrades in place and must not reset settings.
- **Credentials are write-only.**
  - Secrets are encrypted at rest (`internal/secret`) and never returned by the API or included in exports.
  - An empty value on update keeps the stored secret.
  - Secrets never go on a command line; `proxmox-backup-client` reads them from files or `PBS_*_FILE` variables.
- **Small dependency footprint.** Allowed: `modernc.org/sqlite`, `golang.org/x/term`, and `golang.org/x/crypto` (SSH, from milestone 2). Anything else needs a reason in the pull request. Builds use `CGO_ENABLED=0`.
- **The UI is plain JavaScript** in `web/`, embedded in the program. No frameworks, no CDNs.
  - The page runs under a strict CSP: no inline scripts, no `style=""` attributes. Use the utility classes in `app.css`, or set `element.style` from JS.
  - Escape all user content with `esc()`.
- **Network changes that could lock the user out go through the pending/confirm mechanism** in `internal/server/network.go`.
- **User-facing text is plain and specific:** say what happened and what to do next. No jargon in the UI.

## Commands

```bash
make dev      # run locally on http://127.0.0.1:8099 (prints the setup code)
make test     # go test -race ./...
make lint     # gofmt, go vet, staticcheck, shellcheck
make check    # both
make dist     # linux amd64/arm64 release archives in dist/
```

## How to make changes

1. Work on a branch and open a pull request against `v2`.
2. Add or update tests with every behaviour change. New endpoints get coverage in `internal/server/server_test.go`, which runs the real server.
3. Keep `make check` green.
4. Update the relevant page in `docs/` and add an entry under `## [Unreleased]` in `CHANGELOG.md`.

## Versions and releases

- After 2.0.0: bug fixes are patch releases (2.0.1), and features and enhancements are minor releases (2.1.0).
- The version lives in `internal/version/VERSION`.
- To release: in a pull request, bump `VERSION` and rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`. After merge, the owner tags `vX.Y.Z` and pushes it. `.github/workflows/release.yml` checks the tag against `VERSION`, builds and publishes.
- See `docs/development.md`.
