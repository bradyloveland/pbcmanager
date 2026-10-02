#!/usr/bin/env bash
# PBC Manager installer
#
# Usage: sudo ./install.sh [options]
#   --port N            Port for the web UI (default 8099)
#   --no-tls            Serve plain HTTP
#   --behind-proxy      Reverse proxy on this same machine: listen on 127.0.0.1 only,
#                       plain HTTP, trust X-Forwarded-* headers from localhost
#   --proxy-ip IP[,IP]  Reverse proxy on another machine or container: listen on every
#                       interface, plain HTTP, trust X-Forwarded-* from these addresses
#   --base-path /path   Serve under a sub-path, e.g. https://example.com/backups/
#   --version X.Y.Z     Install this release instead of the newest 2.x
#   --skip-client       Don't install proxmox-backup-client on this server
#
# Run it from an extracted release folder to install that copy, or on its own to
# download the newest release from GitHub. Re-running it upgrades in place and
# keeps your settings; only the options you pass are changed. Everything else,
# including these options, can be changed later under Settings in the web UI.
set -euo pipefail

REPO="bradyloveland/pbcmanager"
APP_DIR=/opt/pbcm
CONF_DIR=/etc/pbcm
DATA_DIR=/var/lib/pbcm
USER_NAME=pbcm
UNIT=/etc/systemd/system/pbcm.service

NET_ARGS=()
WANT_VERSION=""
INSTALL_CLIENT=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port) NET_ARGS+=(--port "$2"); shift 2 ;;
    --no-tls) NET_ARGS+=(--tls off); shift ;;
    --behind-proxy) NET_ARGS+=(--bind 127.0.0.1 --tls off --trusted-proxies "127.0.0.1,::1"); shift ;;
    --proxy-ip) NET_ARGS+=(--bind "" --tls off --trusted-proxies "127.0.0.1,::1,$2"); shift 2 ;;
    --base-path) NET_ARGS+=(--base-path "$2"); shift 2 ;;
    --version) WANT_VERSION="${2#v}"; shift 2 ;;
    --skip-client) INSTALL_CLIENT=0; shift ;;
    -h|--help) sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "Unknown option: $1 (see --help)"; exit 1 ;;
  esac
done

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
fail() { printf '\033[31m%s\033[0m\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || fail "Run this as root: sudo ./install.sh"
command -v systemctl >/dev/null || fail "This installer needs systemd."
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) fail "This server runs on x86-64 or ARM64 Linux; this machine is $(uname -m)." ;;
esac

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fetch() { # url dest
  if command -v curl >/dev/null; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null; then wget -qO "$2" "$1"
  else fail "Install curl or wget first: apt-get install -y curl"
  fi
}

# ---- the program --------------------------------------------------------------
if [[ -x "$SRC_DIR/pbcm" && -z "$WANT_VERSION" ]]; then
  BIN="$SRC_DIR/pbcm"
  RUNNER_BIN="$SRC_DIR/pbcm-runner"
  say "Installing from $SRC_DIR"
else
  if [[ -z "$WANT_VERSION" ]]; then
    say "Looking up the newest release"
    fetch "https://api.github.com/repos/$REPO/releases?per_page=30" "$WORK/releases.json"
    WANT_VERSION="$(grep -o '"tag_name": *"v2\.[0-9]*\.[0-9]*"' "$WORK/releases.json" | head -n1 | sed 's/.*"v\(.*\)"/\1/')"
    [[ -n "$WANT_VERSION" ]] || fail "Couldn't find a 2.x release on GitHub. Download one from https://github.com/$REPO/releases and run its install.sh."
  fi
  NAME="pbcm-$WANT_VERSION-linux-$ARCH"
  say "Downloading version $WANT_VERSION"
  BASE_URL="https://github.com/$REPO/releases/download/v$WANT_VERSION"
  fetch "$BASE_URL/$NAME.tar.gz" "$WORK/$NAME.tar.gz" || fail "Couldn't download $NAME.tar.gz."
  fetch "$BASE_URL/SHA256SUMS" "$WORK/SHA256SUMS" || fail "Couldn't download the checksums."
  (cd "$WORK" && grep " $NAME.tar.gz\$" SHA256SUMS | sha256sum -c --quiet -) || fail "The download doesn't match its checksum. Try again."
  tar -xzf "$WORK/$NAME.tar.gz" -C "$WORK"
  BIN="$WORK/$NAME/pbcm"
  RUNNER_BIN="$WORK/$NAME/pbcm-runner"
fi
[[ -f "$RUNNER_BIN" ]] || fail "pbcm-runner is missing from the release files."
"$BIN" version >/dev/null || fail "The pbcm program won't run on this machine."
NEW_VERSION="$("$BIN" version)"

# ---- account and folders -------------------------------------------------------
if ! id -u "$USER_NAME" >/dev/null 2>&1; then
  say "Creating the $USER_NAME service account"
  useradd --system --user-group --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$USER_NAME"
fi
install -d -o "$USER_NAME" -g "$USER_NAME" -m 700 "$CONF_DIR" "$DATA_DIR"
# The service owns its program folder so it can update itself from the web UI.
install -d -o "$USER_NAME" -g "$USER_NAME" -m 755 "$APP_DIR"

FRESH=1
[[ -f "$DATA_DIR/pbcm.db" ]] && FRESH=0
OLD_VERSION=""
[[ -x "$APP_DIR/pbcm" ]] && OLD_VERSION="$("$APP_DIR/pbcm" version 2>/dev/null || true)"

systemctl stop pbcm 2>/dev/null || true
install -o "$USER_NAME" -g "$USER_NAME" -m 755 "$BIN" "$APP_DIR/pbcm.new"
mv -f "$APP_DIR/pbcm.new" "$APP_DIR/pbcm"
# The copy sent to clients during setup (always x86-64, like proxmox-backup-client).
install -o "$USER_NAME" -g "$USER_NAME" -m 755 "$RUNNER_BIN" "$APP_DIR/pbcm-runner.new"
mv -f "$APP_DIR/pbcm-runner.new" "$APP_DIR/pbcm-runner"
# The copy for ARM64 clients (Raspberry Pi and the like), from 2.2.0.
if [[ -f "${RUNNER_BIN}-arm64" ]]; then
  install -o "$USER_NAME" -g "$USER_NAME" -m 755 "${RUNNER_BIN}-arm64" "$APP_DIR/pbcm-runner-arm64.new"
  mv -f "$APP_DIR/pbcm-runner-arm64.new" "$APP_DIR/pbcm-runner-arm64"
fi
# The signed manifest lets the server send pbcm-runner updates to clients; the
# scripts are kept so they can be run again later.
REL_DIR="$(dirname "$BIN")"
for f in MANIFEST MANIFEST.sig install.sh uninstall.sh; do
  if [[ "$REL_DIR" -ef "$APP_DIR" ]]; then
    continue # run from the kept copy in the program folder
  elif [[ -f "$REL_DIR/$f" ]]; then
    install -o "$USER_NAME" -g "$USER_NAME" -m 644 "$REL_DIR/$f" "$APP_DIR/$f"
  else
    rm -f "$APP_DIR/$f"
  fi
done
chmod 755 "$APP_DIR/install.sh" "$APP_DIR/uninstall.sh" 2>/dev/null || true

cat > /usr/local/bin/pbcm <<EOF
#!/bin/sh
# Runs pbcm commands as the service account, so its files stay owned by it.
if [ "\$(id -u)" = 0 ]; then exec runuser -u $USER_NAME -- $APP_DIR/pbcm "\$@"; fi
exec $APP_DIR/pbcm "\$@"
EOF
chmod 755 /usr/local/bin/pbcm

# ---- the server's own proxmox-backup-client ----------------------------------------
# Only used to check datastore space and list snapshots. Backups run on clients.
if [[ $INSTALL_CLIENT -eq 1 ]] && ! command -v proxmox-backup-client >/dev/null; then
  say "Installing proxmox-backup-client"
  CODENAME=""
  # shellcheck disable=SC1091
  [[ -r /etc/os-release ]] && . /etc/os-release && CODENAME="${VERSION_CODENAME:-}"
  if command -v apt-get >/dev/null && [[ "$CODENAME" == "bookworm" || "$CODENAME" == "trixie" ]]; then
    if [[ "$CODENAME" == "bookworm" ]]; then
      fetch https://enterprise.proxmox.com/debian/proxmox-release-bookworm.gpg /etc/apt/trusted.gpg.d/proxmox-release-bookworm.gpg
      echo "deb http://download.proxmox.com/debian/pbs-client bookworm main" > /etc/apt/sources.list.d/pbs-client.list
    else
      fetch https://enterprise.proxmox.com/debian/proxmox-archive-keyring-trixie.gpg /usr/share/keyrings/proxmox-archive-keyring.gpg
      cat > /etc/apt/sources.list.d/pbs-client.sources <<'EOF'
Types: deb
URIs: http://download.proxmox.com/debian/pbs-client
Suites: trixie
Components: main
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
EOF
    fi
    apt-get update -q
    apt-get install -y -q proxmox-backup-client
  else
    echo "Couldn't install it automatically on ${PRETTY_NAME:-this system}. Proxmox publishes it for"
    echo "Debian 12 and 13. Datastore space and snapshot lists need it; install it when you can."
  fi
fi

# ---- settings ------------------------------------------------------------------------
if [[ ${#NET_ARGS[@]} -gt 0 ]]; then
  pbcm network "${NET_ARGS[@]}" >/dev/null
  [[ $FRESH -eq 0 ]] && echo "Network settings updated."
fi
if [[ $FRESH -eq 1 ]]; then
  SETUP_CODE="$(pbcm setup-code)"
else
  SETUP_CODE="$(pbcm setup-code | grep -E '^[A-Z0-9]{4}-' || true)"
fi

# ---- service ---------------------------------------------------------------------------
say "Installing the systemd service"
cat > "$UNIT" <<EOF
[Unit]
Description=PBC Manager
After=network-online.target
Wants=network-online.target

[Service]
User=$USER_NAME
Group=$USER_NAME
Environment=HOME=$DATA_DIR
ExecStart=$APP_DIR/pbcm serve
# After an update from the web UI, the previous version (pbcm.prev) counts
# crashes of the new one and puts itself back after three. Otherwise it does
# nothing; the "-" ignores it being missing.
ExecStopPost=-$APP_DIR/pbcm.prev rollback --after-failure
Restart=always
RestartSec=5
UMask=0077
# Ports below 1024 (such as 443) without running as root.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=$CONF_DIR $DATA_DIR $APP_DIR
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=yes

[Install]
WantedBy=multi-user.target
EOF
rm -f /etc/systemd/system/pbcm-rollback.service
systemctl daemon-reload
systemctl enable pbcm >/dev/null 2>&1

started() { systemctl restart pbcm && sleep 2 && systemctl is-active --quiet pbcm; }
if ! started; then
  # Containers without nesting can't use systemd's sandboxing (exit status 226).
  if [[ "$(systemctl show -p ExecMainStatus --value pbcm)" == "226" ]]; then
    echo "This container doesn't allow systemd's sandboxing, so it's turned off for this service."
    echo "Turn on the container's \"nesting\" feature to use it."
    mkdir -p /etc/systemd/system/pbcm.service.d
    cat > /etc/systemd/system/pbcm.service.d/no-sandbox.conf <<'EOF'
[Service]
ProtectSystem=no
ProtectHome=no
PrivateTmp=no
PrivateDevices=no
ProtectKernelTunables=no
ProtectKernelModules=no
ProtectControlGroups=no
EOF
    systemctl daemon-reload
    started || fail "The service didn't start. See: journalctl -u pbcm -n 50"
  else
    fail "The service didn't start. See: journalctl -u pbcm -n 50"
  fi
fi

# ---- done ------------------------------------------------------------------------------
read -r SCHEME PORT BIND BASE < <(pbcm network --show)
[[ "$BASE" == "-" ]] && BASE=""
HOST="$BIND"
if [[ "$BIND" == "-" ]]; then
  HOST="$(hostname -I 2>/dev/null | awk '{print $1}')"
  HOST="${HOST:-$(hostname)}"
fi
[[ "$HOST" == *:* ]] && HOST="[$HOST]"

say "Done."
if [[ -n "$OLD_VERSION" && "$OLD_VERSION" != "$NEW_VERSION" ]]; then
  echo "Upgraded from $OLD_VERSION to $NEW_VERSION. Your settings were kept."
elif [[ $FRESH -eq 0 ]]; then
  echo "Version $NEW_VERSION reinstalled. Your settings were kept."
else
  echo "Version $NEW_VERSION installed."
fi
echo
if [[ "$BIND" == "127.0.0.1" || "$BIND" == "::1" ]]; then
  echo "The web UI listens on $SCHEME://$HOST:$PORT for your reverse proxy."
  [[ -n "$BASE" ]] && echo "Point the proxy's $BASE/ location at that address."
else
  echo "Open $SCHEME://$HOST:$PORT$BASE/"
  [[ "$SCHEME" == "https" ]] && echo "Your browser will warn about the self-signed certificate the first time; that's expected."
fi
if [[ -n "$SETUP_CODE" ]]; then
  echo
  printf 'Setup code: \033[1m%s\033[0m\n' "$SETUP_CODE"
  echo "Enter it in the browser to create the admin account. To see it again: sudo pbcm setup-code"
fi
echo
echo "Logs: journalctl -u pbcm -f"
