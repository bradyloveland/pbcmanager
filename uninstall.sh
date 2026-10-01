#!/usr/bin/env bash
# Removes PBS Backup Manager. Your settings and run history are kept unless you pass --purge.
# Nothing on your Proxmox Backup Server is touched.
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo "Run as root: sudo ./uninstall.sh"; exit 1; }
systemctl disable --now pbs-manager 2>/dev/null || true
rm -f /etc/systemd/system/pbs-manager.service /usr/local/bin/pbs-manager
systemctl daemon-reload
rm -rf /opt/pbs-manager
if [[ "${1:-}" == "--purge" ]]; then
  rm -rf /etc/pbs-manager /var/lib/pbs-manager
  echo "Removed PBS Backup Manager, its settings, credentials and history."
else
  echo "Removed PBS Backup Manager. Settings remain in /etc/pbs-manager and history in /var/lib/pbs-manager."
  echo "Run with --purge to delete them too."
fi
