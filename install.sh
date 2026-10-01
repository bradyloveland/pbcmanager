#!/usr/bin/env bash
# PBS Backup Manager installer
#
# Usage: sudo ./install.sh [options]
#   --port N            Port for the web UI (default 8099)
#   --no-tls            Serve plain HTTP
#   --behind-proxy      Reverse proxy on this same machine: listen on 127.0.0.1 only,
#                       plain HTTP, trust X-Forwarded-* headers from localhost
#   --proxy-ip IP[,IP]  Reverse proxy on another machine or container: listen on all
#                       interfaces, plain HTTP, trust X-Forwarded-* from these addresses
#   --base-path /path   Serve under a sub-path, e.g. https://example.com/backups/
#   --skip-client       Don't install proxmox-backup-client
#
# Re-running the installer upgrades the app and keeps your settings. Only the
# options you pass are changed.
set -euo pipefail

PORT=""
TLS=""
BIND=""
TRUSTED=""
BASE_PATH=""
BASE_PATH_SET=0
INSTALL_CLIENT=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port) PORT="$2"; shift 2 ;;
    --no-tls) TLS=0; shift ;;
    --behind-proxy) TLS=0; BIND="${BIND:-127.0.0.1}"; TRUSTED="${TRUSTED:-127.0.0.1,::1}"; shift ;;
    --proxy-ip) TLS=0; BIND="0.0.0.0"; TRUSTED="127.0.0.1,::1,$2"; shift 2 ;;
    --base-path) BASE_PATH="$2"; BASE_PATH_SET=1; shift 2 ;;
    --skip-client) INSTALL_CLIENT=0; shift ;;
    -h|--help) sed -n '2,17p' "$0"; exit 0 ;;
    *) echo "Unknown option: $1 (see --help)"; exit 1 ;;
  esac
done

APP_DIR=/opt/pbs-manager
CONF_DIR=/etc/pbs-manager
DATA_DIR=/var/lib/pbs-manager
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
fail() { printf '\033[31m%s\033[0m\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || fail "Run this as root: sudo ./install.sh"
command -v python3 >/dev/null || fail "python3 is required. Install it with your package manager first."
python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)' || fail "Python 3.8 or newer is required."
command -v systemctl >/dev/null || fail "This installer needs systemd."

# ---- proxmox-backup-client -------------------------------------------------
if [[ $INSTALL_CLIENT -eq 1 ]] && ! command -v proxmox-backup-client >/dev/null; then
  say "Installing proxmox-backup-client"
  CODENAME=""
  [[ -r /etc/os-release ]] && . /etc/os-release && CODENAME="${VERSION_CODENAME:-}"
  if command -v apt-get >/dev/null && [[ "$CODENAME" == "bookworm" || "$CODENAME" == "trixie" ]]; then
    command -v wget >/dev/null || apt-get install -y wget
    if [[ "$CODENAME" == "bookworm" ]]; then
      wget -qO /etc/apt/trusted.gpg.d/proxmox-release-bookworm.gpg \
        https://enterprise.proxmox.com/debian/proxmox-release-bookworm.gpg
      echo "deb http://download.proxmox.com/debian/pbs-client bookworm main" \
        > /etc/apt/sources.list.d/pbs-client.list
    else
      wget -qO /usr/share/keyrings/proxmox-archive-keyring.gpg \
        https://enterprise.proxmox.com/debian/proxmox-archive-keyring-trixie.gpg
      cat > /etc/apt/sources.list.d/pbs-client.sources <<'EOF'
Types: deb
URIs: http://download.proxmox.com/debian/pbs-client
Suites: trixie
Components: main
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
EOF
    fi
    apt-get update
    apt-get install -y proxmox-backup-client
  else
    echo "Couldn't install it automatically on this system (${PRETTY_NAME:-unknown})."
    echo "Proxmox publishes packages for Debian bookworm and trixie. Install it yourself;"
    echo "the manager will pick it up once it's on the PATH."
  fi
fi

# ---- files -----------------------------------------------------------------
say "Copying files to $APP_DIR"
install -d -m 755 "$APP_DIR" "$APP_DIR/static"
install -m 755 "$SRC_DIR/app.py" "$APP_DIR/app.py"
install -m 644 "$SRC_DIR/qr.py" "$APP_DIR/qr.py"
install -m 644 "$SRC_DIR/static/index.html" "$APP_DIR/static/index.html"
install -d -m 700 "$CONF_DIR" "$DATA_DIR" "$DATA_DIR/logs"

cat > /usr/local/bin/pbs-manager <<EOF
#!/bin/sh
exec /usr/bin/env python3 $APP_DIR/app.py "\$@"
EOF
chmod 755 /usr/local/bin/pbs-manager

# ---- settings ----------------------------------------------------------------
FRESH=1
[[ -f "$CONF_DIR/config.json" ]] && FRESH=0
[[ $FRESH -eq 1 ]] && TLS="${TLS:-1}" && PORT="${PORT:-8099}"

ARGS=()
[[ -n "$PORT" ]] && ARGS+=(--port "$PORT")
[[ -n "$BIND" ]] && ARGS+=(--bind "$BIND")
[[ -n "$TRUSTED" ]] && ARGS+=(--trusted-proxies "$TRUSTED")
[[ $BASE_PATH_SET -eq 1 ]] && ARGS+=(--base-path "$BASE_PATH")
if [[ "$TLS" == "1" ]]; then
  if command -v openssl >/dev/null; then
    if [[ ! -f "$CONF_DIR/tls.crt" ]]; then
      say "Creating a self-signed TLS certificate"
      HOST_FQDN="$(hostname -f 2>/dev/null || hostname)"
      openssl req -x509 -newkey rsa:3072 -sha256 -days 3650 -nodes \
        -keyout "$CONF_DIR/tls.key" -out "$CONF_DIR/tls.crt" \
        -subj "/CN=$HOST_FQDN" \
        -addext "subjectAltName=DNS:$HOST_FQDN,DNS:$(hostname)" >/dev/null 2>&1
      chmod 600 "$CONF_DIR/tls.key"
    fi
    ARGS+=(--tls-cert "$CONF_DIR/tls.crt" --tls-key "$CONF_DIR/tls.key")
  else
    echo "openssl not found, so the web UI will use plain HTTP."
    ARGS+=(--tls-cert "" --tls-key "")
  fi
elif [[ "$TLS" == "0" ]]; then
  ARGS+=(--tls-cert "" --tls-key "")
fi
[[ ${#ARGS[@]} -gt 0 ]] && pbs-manager configure "${ARGS[@]}" >/dev/null
[[ $FRESH -eq 0 ]] && echo "Existing settings kept${ARGS[*]:+ (updated: ${ARGS[*]})}."

if ! grep -q '"password_hash": "pbkdf2' "$CONF_DIR/config.json" 2>/dev/null; then
  say "Choose a password for the web UI (user: admin)"
  pbs-manager passwd
fi

# ---- service -----------------------------------------------------------------
say "Installing the systemd service"
cat > /etc/systemd/system/pbs-manager.service <<EOF
[Unit]
Description=PBS Backup Manager
After=network-online.target local-fs.target remote-fs.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/env python3 $APP_DIR/app.py serve
Restart=on-failure
RestartSec=5
KillMode=mixed
TimeoutStopSec=30
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable pbs-manager >/dev/null 2>&1
systemctl restart pbs-manager
sleep 1

read -r CUR_PORT CUR_TLS CUR_BIND CUR_BASE < <(python3 - "$CONF_DIR/config.json" <<'PY'
import json, sys
s = json.load(open(sys.argv[1]))["server"]
print(s.get("port", 8099), 1 if s.get("tls_cert") else 0, s.get("bind", "0.0.0.0"), s.get("base_path") or "-")
PY
)
SCHEME=$([[ "$CUR_TLS" == "1" ]] && echo https || echo http)
IP=$(hostname -I 2>/dev/null | awk '{print $1}')
say "Done."
systemctl --no-pager --lines=0 status pbs-manager | head -3 || true
echo
if [[ "$CUR_BIND" == "127.0.0.1" ]]; then
  echo "The UI listens on http://127.0.0.1:${CUR_PORT} for your reverse proxy."
  if [[ "$CUR_BASE" != "-" ]]; then
    echo "Point the proxy's ${CUR_BASE}/ location at that address."
  else
    echo "Point the proxy at that address."
  fi
else
  echo "Open ${SCHEME}://${IP:-this-machine}:${CUR_PORT}$([[ "$CUR_BASE" != "-" ]] && echo "${CUR_BASE}")/ and sign in."
  [[ "$CUR_TLS" == "1" ]] && echo "Your browser will warn about the self-signed certificate the first time; that's expected."
fi
echo "Turn on two-step verification under Account once you're signed in."
echo "Logs: journalctl -u pbs-manager -f"
