#!/usr/bin/env bash
# Removes Proxmox Backup Client Web Manager from this server.
#
# Usage: sudo ./uninstall.sh [--purge]
#   --purge   also delete the settings, database, certificates and the pbcwm account
#
# Without --purge, /etc/pbcwm and /var/lib/pbcwm are kept so a reinstall picks up
# where you left off. Clients aren't touched: they keep backing up on their own.
set -euo pipefail

PURGE=0
case "${1:-}" in
  --purge) PURGE=1 ;;
  "") ;;
  -h|--help) sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  *) echo "Unknown option: $1 (see --help)"; exit 1 ;;
esac
[[ $EUID -eq 0 ]] || { echo "Run this as root: sudo ./uninstall.sh" >&2; exit 1; }

systemctl disable --now pbcwm 2>/dev/null || true
rm -rf /etc/systemd/system/pbcwm.service /etc/systemd/system/pbcwm.service.d
systemctl daemon-reload
rm -f /usr/local/bin/pbcwm
rm -rf /opt/pbcwm

if [[ $PURGE -eq 1 ]]; then
  rm -rf /etc/pbcwm /var/lib/pbcwm
  if id -u pbcwm >/dev/null 2>&1; then userdel pbcwm 2>/dev/null || true; fi
  getent group pbcwm >/dev/null && groupdel pbcwm 2>/dev/null || true
  echo "Removed, including all settings and data."
else
  echo "Removed. Settings and data are still in /etc/pbcwm and /var/lib/pbcwm."
  echo "Run with --purge to delete them too."
fi
