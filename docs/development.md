# Development

## Layout

```
cmd/pbcwm/          The server program and its commands (serve, setup-code, passwd, totp-reset, network)
internal/auth/      Password hashing, TOTP, recovery codes, sign-in throttling
internal/config/    Every setting: definitions, defaults, checks. Network settings.
internal/qr/        QR codes for authenticator enrolment (standard library only)
internal/secret/    Encryption for secrets stored in the database
internal/server/    HTTP server, API, listeners, network change confirm/undo
internal/store/     SQLite database and its migrations
internal/tlscert/   Self-signed certificates, checking uploaded ones
internal/version/   VERSION file, product name
web/                The browser UI (embedded in the program)
install.sh          Installer and upgrader for the server
uninstall.sh
scripts/            Release helpers
docs/               Documentation, including the version 2 design
```

## Architecture (milestone 1)

```
Browser ── HTTP(S) ──> sniffing listener(s) ──> Server.ServeHTTP
                                               │  which settings did this request arrive under?
                                               │  strip base path, work out client IP and HTTPS
                                               ▼
                                         http.ServeMux → api_* handlers → store (SQLite)
```

- **One program, no runtime dependencies.** The web UI is embedded with `go:embed`. The SQLite driver (`modernc.org/sqlite`) is pure Go, so `CGO_ENABLED=0` builds run on any Linux.
- **Settings** live in the `settings` table as JSON. Each one is defined once in `internal/config/settings.go`, and the Settings page is generated from those definitions. A new setting needs a definition and the code that reads it, nothing else.
- **Secrets** (the TOTP secret for now; tokens and passwords later) are encrypted with AES-256-GCM using `/etc/pbcwm/secret.key`. They're never returned by the API.
- **Sessions** are stored by the SHA-256 of their token, so they survive restarts and a database copy can't be used to sign in.
- **Network changes that could lock you out** (address, port, HTTPS, base path) are *pending* until confirmed from the new address. While pending:
  - The server answers on both the old and the new settings.
  - Each listener tells HTTPS from plain HTTP by the first byte of the connection. That's how one port can serve both while the change waits, and how `http://` requests get redirected to `https://`.
  - If nobody confirms within about two minutes, the server goes back to the old settings.
  - A pending change is never saved, so a restart also undoes it.

## Running locally

```bash
make dev
```

This runs the server on `http://127.0.0.1:8099` with throwaway settings in `tmp/dev/` and prints the setup code. Delete `tmp/dev` to start over.

## Checks

```bash
make test     # go test -race ./...
make lint     # gofmt, go vet, staticcheck, shellcheck
make check    # both
make dist     # release archives for linux/amd64 and linux/arm64 in dist/
```

Install the tools with `brew install go shellcheck` (or your package manager) and `go install honnef.co/go/tools/cmd/staticcheck@latest`.

| Package | Tests cover |
| --- | --- |
| `auth` | PBKDF2 (including a 1.x hash), TOTP against the RFC 6238 vectors, drift window, replay, recovery codes, throttling |
| `qr` | Exact match with the 1.x generator (whose codes were decode-checked), finder and timing patterns |
| `secret` | Encryption round trip, key file, wrong key and tampering |
| `store` | Migrations, settings, admin secrets encrypted at rest, session idle expiry |
| `config` | Every setting's checks and defaults, network settings |
| `tlscert` | Self-signed certificates, mismatched or junk uploads |
| `server` | The real server on local ports. Covers: <ul><li>setup code and throttling</li><li>sign-in, cookies, CSRF header</li><li>two-step sign-in with replay and recovery codes</li><li>sessions surviving a restart</li><li>settings</li><li>base path and trusted proxies</li><li>HTTPS and the HTTP redirect</li><li>network changes confirmed, undone, timed out, blocked by a busy port</li><li>switching to an uploaded certificate on the same port</li></ul> |

CI (`.github/workflows/ci.yml`) also installs the built package on an Ubuntu runner with systemd. It checks that the server answers over HTTPS and finishes setup through the API. Then it upgrades in place with a new port, and uninstalls with `--purge`.

## Making changes

- Work on a branch and open a pull request against `v2` (or `main` once 2.0.0 ships).
- Add tests with every behaviour change. New endpoints get coverage in `internal/server/server_test.go`.
- Keep `make check` green.
- Update `docs/` and add an entry under `## [Unreleased]` in `CHANGELOG.md`.
- Write user-facing text plainly: what happened and what to do next.

## Releases

Version 2 releases are tagged `v2.X.Y`. Bug fixes are patch releases (2.0.1) and features are minor releases (2.1.0).

1. In a pull request:
   - set `internal/version/VERSION` to `X.Y.Z`
   - rename `## [Unreleased]` in `CHANGELOG.md` to `## [X.Y.Z] - YYYY-MM-DD`
   - add the link reference at the bottom of `CHANGELOG.md`
2. After it's merged, tag the merge commit and push the tag:
   ```bash
   git tag -a vX.Y.Z -m "Proxmox Backup Client Web Manager X.Y.Z"
   git push origin vX.Y.Z
   ```
3. `.github/workflows/release.yml` runs the tests, checks the tag matches `VERSION`, builds the archives with `SHA256SUMS`, and publishes the release with that version's changelog section. Versions with a `-` (like `2.0.0-rc.1`) are marked as pre-releases.

Releases will be signed once self-update lands (milestone 5).
