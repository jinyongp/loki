#!/bin/sh
# A private rollback snapshot of Python deployment files and central state.
set -eu
test "$(id -u)" -eq 0
DEST=${1:?backup directory required}
case "$DEST" in /var/backups/loki/python-[0-9]*) ;; *) exit 2 ;; esac
test ! -e "$DEST"
umask 077
mkdir -p "$DEST"
# Stopping MCP also stops its dependent Cloudflare tunnel. Capture the active
# services before stopping anything, and restore them even when stop fails.
restore_services=
for service in loki-runtime.service loki-mcp.service loki-cloudflared.service; do
  if systemctl is-active --quiet "$service"; then
    restore_services="$restore_services $service"
  fi
done
restore() {
  result=$?
  trap - EXIT HUP INT TERM
  for service in $restore_services; do
    if ! systemctl start "$service"; then
      echo "Failed to restore $service after backup" >&2
      result=1
    fi
  done
  exit "$result"
}
trap restore EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
systemctl stop loki-mcp.service loki-runtime.service
cd /
tar -czpf "$DEST/deployment.tar.gz" -C / \
  opt/loki-mcp opt/loki-browser/venv etc/loki \
  usr/local/bin/loki usr/local/libexec/loki-* \
  var/lib/loki/runtime var/lib/loki/project-state \
  etc/systemd/system/loki-*.service etc/systemd/system/loki-*.timer
mkdir "$DEST/restore-check"
tar -xzpf "$DEST/deployment.tar.gz" -C "$DEST/restore-check"
tar -dzf "$DEST/deployment.tar.gz" -C "$DEST/restore-check"
sha256sum "$DEST/deployment.tar.gz"
echo "Backup and isolated restoration check passed: $DEST"
