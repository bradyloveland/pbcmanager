#!/bin/bash
# Sets this machine up as a PBC Manager client. The
# server uploads this script with pbcm-runner and the server's public key
# into a temporary folder, runs it as root, and it deletes the folder when done.
#
# It installs proxmox-backup-client if missing, creates the pbcm account
# (signs in only with the server's key, and can only run pbcm-runner), and
# installs pbcm-runner with a sudo rule allowing that one program.
set -euo pipefail
exec 2>&1

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
trap 'rm -rf "$DIR"' EXIT
RUNNER=/usr/local/lib/pbcm/pbcm-runner
ACCOUNT=pbcm
MARKER='command="sudo -n /usr/local/lib/pbcm/pbcm-runner ssh"'

step() { echo "==> $*"; }
fail() { echo "ERROR: $*"; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "Setup must run as root."
command -v systemctl >/dev/null 2>&1 || fail "This machine doesn't use systemd. Clients need systemd to run backups on their own schedule."

ARCH="$(uname -m)"
OS_ID="" OS_CODENAME="" OS_PRETTY="$(uname -s)"
if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  OS_ID="${ID:-}" OS_CODENAME="${VERSION_CODENAME:-}" OS_PRETTY="${PRETTY_NAME:-$OS_PRETTY}"
fi
step "Setting up $OS_PRETTY ($ARCH)"

APT_UPDATED=0
apt_install() {
  command -v apt-get >/dev/null 2>&1 || fail "Can't install $* automatically because this isn't an apt-based system. Install it by hand, then use Repair."
  export DEBIAN_FRONTEND=noninteractive
  if [ "$APT_UPDATED" -eq 0 ]; then
    apt-get update -q
    APT_UPDATED=1
  fi
  apt-get install -y -q --no-install-recommends "$@"
}

# Download a Proxmox signing key and check it against the checksum this
# release of the server expects, so a tampered download is never trusted.
fetch_key() { # url destination sha256
  local tmp="$DIR/key.gpg"
  curl -fsSL "$1" -o "$tmp" || fail "Couldn't download the Proxmox signing key from $1."
  echo "$3  $tmp" | sha256sum -c --quiet - >/dev/null 2>&1 ||
    fail "The Proxmox signing key from $1 doesn't match the expected checksum, so nothing was installed."
  install -m 644 "$tmp" "$2"
}

# ---- proxmox-backup-client ---------------------------------------------------
if command -v proxmox-backup-client >/dev/null 2>&1; then
  step "proxmox-backup-client is already installed: $(proxmox-backup-client version 2>/dev/null | head -n1)"
else
  [ "$ARCH" = x86_64 ] || fail "proxmox-backup-client is only made for x86-64 machines, and this one is $ARCH."
  command -v apt-get >/dev/null 2>&1 ||
    fail "proxmox-backup-client can only be installed automatically on apt-based systems. Install Proxmox's static client by hand, then use Repair."
  step "Installing proxmox-backup-client"
  apt_install ca-certificates curl
  KEYRING=/usr/share/keyrings/pbcm-proxmox-archive-keyring.gpg
  case "$OS_ID:$OS_CODENAME" in
    debian:trixie|debian:bookworm) SUITE="$OS_CODENAME" PKG=proxmox-backup-client ;;
    debian:bullseye) SUITE=bullseye PKG=proxmox-backup-client KEYRING=/usr/share/keyrings/pbcm-proxmox-release-bullseye.gpg ;;
    *)
      SUITE=bookworm PKG=proxmox-backup-client-static
      step "Proxmox has no package made for $OS_PRETTY, so using its static build"
      ;;
  esac
  if [ "$SUITE" = bullseye ]; then
    fetch_key https://enterprise.proxmox.com/debian/proxmox-release-bullseye.gpg "$KEYRING" \
      411b420c3ab024d099e1ef55d06695a9d90a7db7c49b76fa719b453eb376093e
  else
    fetch_key https://enterprise.proxmox.com/debian/proxmox-archive-keyring-trixie.gpg "$KEYRING" \
      136673be77aba35dcce385b28737689ad64fd785a797e57897589aed08db6e45
  fi
  echo "deb [signed-by=$KEYRING] http://download.proxmox.com/debian/pbs-client $SUITE main" \
    > /etc/apt/sources.list.d/pbcm-pbs-client.list
  APT_UPDATED=0
  apt_install "$PKG"
  step "Installed $(proxmox-backup-client version 2>/dev/null | head -n1)"
fi

# ---- sudo ------------------------------------------------------------------
command -v sudo >/dev/null 2>&1 || { step "Installing sudo"; apt_install sudo; }
grep -Eq '^[[:space:]]*[@#]includedir[[:space:]]+/etc/sudoers\.d' /etc/sudoers ||
  fail "/etc/sudoers doesn't read /etc/sudoers.d, so the pbcm sudo rule wouldn't take effect. Add \"@includedir /etc/sudoers.d\" to it."

# ---- the pbcm account -------------------------------------------------------
if ! id "$ACCOUNT" >/dev/null 2>&1; then
  useradd --system --user-group --create-home --home-dir /var/lib/pbcm --shell /bin/sh "$ACCOUNT"
  step "Created the $ACCOUNT account"
fi
# A shell is needed to run the forced command; "*" means no password at all
# (not "locked", which some SSH setups refuse even for key sign-in).
usermod --shell /bin/sh --password '*' "$ACCOUNT"
HOME_DIR="$(getent passwd "$ACCOUNT" | cut -d: -f6)"
install -d -o "$ACCOUNT" -g "$ACCOUNT" -m 700 "$HOME_DIR/.ssh"
AK="$HOME_DIR/.ssh/authorized_keys"
touch "$AK"
{ grep -vF "$MARKER" "$AK" || true; echo "restrict,$MARKER $(cat "$DIR/key.pub")"; } > "$AK.new"
chown "$ACCOUNT:$ACCOUNT" "$AK.new"
chmod 600 "$AK.new"
mv -f "$AK.new" "$AK"
step "The $ACCOUNT account signs in only with this server's key and can only run pbcm-runner"

# ---- pbcm-runner and its sudo rule --------------------------------------------
install -d -o root -g root -m 755 "$(dirname "$RUNNER")"
install -o root -g root -m 755 "$DIR/pbcm-runner" "$RUNNER"
step "Installed pbcm-runner $("$RUNNER" version)"

cat > "$DIR/sudoers" <<EOF
# Managed by PBC Manager.
# The $ACCOUNT account may run pbcm-runner as root, and nothing else.
Defaults:$ACCOUNT !requiretty
Defaults:$ACCOUNT env_keep += "SSH_ORIGINAL_COMMAND"
$ACCOUNT ALL=(root) NOPASSWD: $RUNNER
EOF
visudo -cf "$DIR/sudoers" >/dev/null || fail "The sudo rule didn't pass visudo's check, so it wasn't installed."
install -o root -g root -m 440 "$DIR/sudoers" /etc/sudoers.d/pbcm

install -d -o root -g root -m 700 /etc/pbcm/client /var/lib/pbcm/client
step "Setup finished"
