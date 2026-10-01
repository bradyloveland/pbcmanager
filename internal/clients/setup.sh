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
OS_ID="" OS_PRETTY="$(uname -s)"
if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  OS_ID="${ID:-}" OS_PRETTY="${PRETTY_NAME:-$OS_PRETTY}"
fi
step "Setting up $OS_PRETTY ($ARCH)"

# Clients must be Debian 12 or 13, or based on them (Proxmox VE, OMV, Ubuntu
# and so on). /etc/debian_version shows the base: "12.7", "13.1", or
# "bookworm/sid" and "trixie/sid" on derivatives.
BASE=""
[ -r /etc/debian_version ] && case "$(cat /etc/debian_version)" in
  12.*|bookworm*) BASE=bookworm ;;
  13.*|trixie*) BASE=trixie ;;
esac
[ -n "$BASE" ] || fail "$OS_PRETTY isn't supported. Clients need Debian 12 or 13, or a system based on them, such as Proxmox VE 8 or 9, OpenMediaVault 7 or 8, or Ubuntu 22.04 or 24.04."
command -v apt-get >/dev/null 2>&1 || fail "apt-get is missing, so this doesn't look like a normal Debian-based system."

APT_UPDATED=0
apt_install() {
  export DEBIAN_FRONTEND=noninteractive
  if [ "$APT_UPDATED" -eq 0 ]; then
    apt-get update -q ||
      fail "apt-get update failed, so nothing could be installed. Fix this machine's package sources, then use Repair."
    APT_UPDATED=1
  fi
  apt-get install -y -q --no-install-recommends "$@"
}

# Download the Proxmox signing key and check it against the checksum this
# release of the server expects, so a tampered download is never trusted.
KEYRING=/usr/share/keyrings/pbcm-proxmox-archive-keyring.gpg
fetch_key() {
  local tmp="$DIR/key.gpg" url=https://enterprise.proxmox.com/debian/proxmox-archive-keyring-trixie.gpg
  curl -fsSL "$url" -o "$tmp" || fail "Couldn't download the Proxmox signing key from $url."
  echo "136673be77aba35dcce385b28737689ad64fd785a797e57897589aed08db6e45  $tmp" | sha256sum -c --quiet - >/dev/null 2>&1 ||
    fail "The Proxmox signing key from $url doesn't match the expected checksum, so nothing was installed."
  install -m 644 "$tmp" "$KEYRING"
}

# ---- proxmox-backup-client ---------------------------------------------------
if command -v proxmox-backup-client >/dev/null 2>&1; then
  step "proxmox-backup-client is already installed: $(proxmox-backup-client version 2>/dev/null | head -n1)"
else
  [ "$ARCH" = x86_64 ] || fail "proxmox-backup-client is only made for x86-64 machines, and this one is $ARCH."
  step "Installing proxmox-backup-client"
  apt_install ca-certificates curl
  if [ "$OS_ID" = debian ]; then
    PKG=proxmox-backup-client
  else
    # Derivatives can have older libraries than the Debian package expects;
    # the static build has no such dependencies.
    PKG=proxmox-backup-client-static
    step "$OS_PRETTY is based on Debian $BASE, so using Proxmox's static build"
  fi
  fetch_key
  echo "deb [signed-by=$KEYRING] http://download.proxmox.com/debian/pbs-client $BASE main" \
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

# ---- SSH access ---------------------------------------------------------------
# Some systems only let certain groups or users sign in over SSH. OpenMediaVault,
# for one, has "AllowGroups root _ssh". sshd -T shows the settings that apply
# to the pbcm account. Joining an allowed group is safe: the account can still
# only sign in with the server's key and run pbcm-runner. sshd_config itself
# is never edited.
SSHD="$(command -v sshd 2>/dev/null || echo /usr/sbin/sshd)"
if [ -x "$SSHD" ] && SSHD_CONF="$("$SSHD" -T -C "user=$ACCOUNT,host=localhost,addr=127.0.0.1" 2>/dev/null)"; then
  # The values of a setting; sshd -T may print one line per value.
  sshd_values() { printf '%s\n' "$SSHD_CONF" | awk -v k="$1" 'tolower($1) == k { for (i = 2; i <= NF; i++) print $i }'; }
  # matches NAME PATTERN...: sshd's pattern rules, without negation.
  matches() {
    local name="$1" pat
    shift
    for pat in "$@"; do
      pat="${pat%%@*}"
      # shellcheck disable=SC2053 # the pattern is meant to be a glob
      [[ "$pat" != !* && "$name" == $pat ]] && return 0
    done
    return 1
  }
  mapfile -t ALLOW_GROUPS < <(sshd_values allowgroups)
  mapfile -t ALLOW_USERS < <(sshd_values allowusers)
  mapfile -t DENY_GROUPS < <(sshd_values denygroups)
  mapfile -t DENY_USERS < <(sshd_values denyusers)
  if [ "${#DENY_USERS[@]}" -gt 0 ] && matches "$ACCOUNT" "${DENY_USERS[@]}"; then
    step "WARNING: this machine's SSH settings deny the $ACCOUNT account (DenyUsers ${DENY_USERS[*]}). Remove it from DenyUsers in /etc/ssh/sshd_config, run \"systemctl reload ssh\", then use Repair."
  fi
  if [ "${#ALLOW_GROUPS[@]}" -gt 0 ]; then
    in_allowed=0
    for g in $(id -nG "$ACCOUNT"); do
      matches "$g" "${ALLOW_GROUPS[@]}" && in_allowed=1
    done
    if [ "$in_allowed" -eq 0 ]; then
      chosen=""
      for g in "${ALLOW_GROUPS[@]}"; do
        # A plain group name that exists, and never one that grants admin
        # rights (that would let the account get round its sudo rule).
        case "$g" in *[*?!]*|root|sudo|wheel|admin|adm|staff|docker|lxd|incus|libvirt|disk|shadow|kvm) continue ;; esac
        getent group "$g" >/dev/null 2>&1 && { chosen="$g"; break; }
      done
      if [ -n "$chosen" ]; then
        usermod -aG "$chosen" "$ACCOUNT"
        step "Added $ACCOUNT to the $chosen group, which this machine's SSH settings allow to sign in (AllowGroups ${ALLOW_GROUPS[*]})"
      else
        step "WARNING: this machine's SSH settings only allow the groups ${ALLOW_GROUPS[*]} to sign in, and none of them suits the $ACCOUNT account. Create a group for it, add it to AllowGroups in /etc/ssh/sshd_config, run \"usermod -aG <group> $ACCOUNT\" and \"systemctl reload ssh\", then use Repair."
      fi
    fi
  fi
  if [ "${#ALLOW_USERS[@]}" -gt 0 ] && ! matches "$ACCOUNT" "${ALLOW_USERS[@]}"; then
    step "WARNING: this machine's SSH settings only allow certain users to sign in (AllowUsers ${ALLOW_USERS[*]}). Add $ACCOUNT to the AllowUsers line in /etc/ssh/sshd_config, run \"systemctl reload ssh\", then use Repair."
  fi
  if [ "${#DENY_GROUPS[@]}" -gt 0 ]; then
    for g in $(id -nG "$ACCOUNT"); do
      if matches "$g" "${DENY_GROUPS[@]}"; then
        step "WARNING: the $ACCOUNT account is in the $g group, which this machine's SSH settings deny (DenyGroups ${DENY_GROUPS[*]})."
      fi
    done
  fi
fi

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
