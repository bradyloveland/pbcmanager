#!/usr/bin/env bash
# Removes PBC Manager from this server.
#
# Usage: sudo ./uninstall.sh [--purge]
#   --purge   also delete the settings, database, certificates and the pbcm account
#
# Without --purge, /etc/pbcm and /var/lib/pbcm are kept so a reinstall picks up
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

systemctl disable --now pbcm 2>/dev/null || true
rm -rf /etc/systemd/system/pbcm.service /etc/systemd/system/pbcm.service.d
systemctl daemon-reload
rm -f /usr/local/bin/pbcm
rm -rf /opt/pbcm

if [[ $PURGE -eq 1 ]]; then
  rm -rf /etc/pbcm /var/lib/pbcm
  if id -u pbcm >/dev/null 2>&1; then userdel pbcm 2>/dev/null || true; fi
  if getent group pbcm >/dev/null; then groupdel pbcm 2>/dev/null || true; fi
  echo "Removed, including all settings and data."
else
  echo "Removed. Settings and data are still in /etc/pbcm and /var/lib/pbcm."
  echo "Run with --purge to delete them too."
fi
