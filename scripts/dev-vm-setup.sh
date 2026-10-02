#!/usr/bin/env bash
# Sets up a Debian 13 VM for PBC Manager development, driven by Claude Code
# Remote Control from the Claude desktop or iOS app. No desktop needed: run it
# over SSH on a fresh VM, then follow the steps it prints.
#
#   sudo bash dev-vm-setup.sh [--user NAME] [--passwordless-sudo]
#
#   --user NAME            the account that does the development (default: the
#                          user who ran sudo)
#   --passwordless-sudo    let that account use sudo without a password, so
#                          Claude can run the install and update tests. Only
#                          on a VM used for nothing else.
#
# Installs git, make, Go (latest, checked against go.dev's checksums), Docker,
# the shell and Go linters, gh, tmux and the claude CLI, clones the
# repository, and adds a claude-remote-control service. Safe to run again: it updates
# what's there.
set -euo pipefail

REPO_URL=https://github.com/bradyloveland/pbcmanager.git
UNIT=claude-remote-control

say() { printf '\n==> %s\n' "$*"; }
die() { printf 'Error: %s\n' "$*" >&2; exit 1; }

user="${SUDO_USER:-}"
nopass=0
while [ $# -gt 0 ]; do
  case "$1" in
    --user) [ $# -ge 2 ] || die "--user needs a name"; user="$2"; shift 2 ;;
    --passwordless-sudo) nopass=1; shift ;;
    -h|--help) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option $1 (see --help)" ;;
  esac
done

[ "$(id -u)" = 0 ] || die "run this as root: sudo bash $0"
if [ -z "$user" ] || [ "$user" = root ]; then
  die "say which account does the development: --user NAME (not root)"
fi
id "$user" >/dev/null 2>&1 || die "there's no account called $user. Create it first: adduser $user"
# shellcheck source=/dev/null
. /etc/os-release
if [ "${ID:-}" != debian ] || [ "${VERSION_ID%%.*}" -lt 13 ] 2>/dev/null; then
  echo "Warning: this is written for Debian 13; found ${PRETTY_NAME:-an unknown system}."
fi
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac
home="$(getent passwd "$user" | cut -d: -f6)"
userpath="/usr/local/go/bin:$home/go/bin:$home/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
as_user() { sudo -u "$user" -H env HOME="$home" PATH="$userpath" "$@"; }

say "Installing packages"
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
  git make curl ca-certificates jq tmux shellcheck docker.io gh sudo >/dev/null

say "Installing Go"
want="$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -1)"
have="$(/usr/local/go/bin/go env GOVERSION 2>/dev/null || true)"
if [ "$have" = "$want" ]; then
  echo "$want is already installed."
else
  file="$want.linux-$arch.tar.gz"
  sum="$(curl -fsSL 'https://go.dev/dl/?mode=json' | jq -r --arg f "$file" '.[].files[] | select(.filename == $f) | .sha256')"
  [ -n "$sum" ] || die "go.dev doesn't list $file"
  tmp="$(mktemp -d)"
  curl -fsSL -o "$tmp/$file" "https://go.dev/dl/$file"
  echo "$sum  $tmp/$file" | sha256sum -c --quiet || die "the Go download doesn't match go.dev's checksum"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$tmp/$file"
  rm -rf "$tmp"
  echo "Installed $want."
fi
cat > /etc/profile.d/pbcm-dev.sh <<'EOF'
# PBC Manager development: Go, Go tools and the claude CLI on PATH, and
# make dev reachable from other machines on the network.
export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin"
export DEV_BIND=0.0.0.0
EOF

say "Setting up Docker and sudo for $user"
systemctl enable --now docker >/dev/null 2>&1 || echo "Warning: couldn't start Docker. Check: systemctl status docker"
usermod -aG docker,sudo "$user"
if [ "$nopass" = 1 ]; then
  echo "$user ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/90-pbcm-dev
  chmod 440 /etc/sudoers.d/90-pbcm-dev
  visudo -cqf /etc/sudoers.d/90-pbcm-dev || { rm -f /etc/sudoers.d/90-pbcm-dev; die "the sudo rule didn't check out"; }
  echo "$user can use sudo without a password."
fi

say "Installing staticcheck"
as_user go install honnef.co/go/tools/cmd/staticcheck@latest

say "Installing the claude CLI"
if [ -x "$home/.local/bin/claude" ]; then
  echo "Already installed; it updates itself."
else
  as_user bash -c 'curl -fsSL https://claude.ai/install.sh | bash'
fi

say "Getting the repository"
if [ -d "$home/pbcmanager/.git" ]; then
  echo "$home/pbcmanager is already there."
else
  as_user git clone -q "$REPO_URL" "$home/pbcmanager"
fi
as_user bash -c "cd '$home/pbcmanager' && go build ./... && echo 'It builds.'"

say "Adding the $UNIT service"
# Remote Control runs inside tmux (on its own socket, so it doesn't mix with
# your tmux sessions), which gives it a terminal you can attach to:
#   tmux -L claude attach     (Ctrl-b d to leave it running)
cat > "/etc/systemd/system/$UNIT.service" <<EOF
[Unit]
Description=Claude Code Remote Control for PBC Manager development
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=forking
User=$user
WorkingDirectory=$home/pbcmanager
Environment=HOME=$home
Environment=PATH=$userpath
Environment=DEV_BIND=0.0.0.0
ExecStart=/usr/bin/tmux -L claude new-session -d -s claude $home/.local/bin/claude remote-control
ExecStop=/usr/bin/tmux -L claude kill-server
Restart=always
RestartSec=30

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload || echo "Warning: systemd isn't running, so the service can't be started here."

ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
cat <<EOF

Done. Next, as $user (log out and back in first, so the docker group applies):

  1. Sign in to Claude, and trust the project folder when asked:
       cd ~/pbcmanager && claude
     Then leave it with /exit.
  2. Sign in to GitHub, and let gh set up git:
       gh auth login
       git config --global user.name "Your Name"
       git config --global user.email you@example.com
  3. Start Remote Control, now and at every boot:
       sudo systemctl enable --now $UNIT
  4. In the Claude desktop or iOS app, open Code: the session is in the list.
     To see its terminal here: tmux -L claude attach (Ctrl-b d to leave).

make dev on this VM listens on all addresses: open http://${ip:-this-vm}:8099
from your Mac or phone.
EOF
