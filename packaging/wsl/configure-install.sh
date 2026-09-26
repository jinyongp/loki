#!/bin/sh
set -eu
umask 077

state_root=/var/lib/loki-appliance
port_file=$state_root/mcp-port
marker=$state_root/provisioned
port=${1:-}

case "$port" in
  ''|*[!0-9]*)
    echo "loki-appliance: MCP port must be an integer" >&2
    exit 2
    ;;
esac
if test "$port" -lt 1024 || test "$port" -gt 65535; then
  echo "loki-appliance: MCP port must be between 1024 and 65535" >&2
  exit 2
fi
if test -e "$marker"; then
  echo "loki-appliance: installation is already provisioned" >&2
  exit 1
fi

install -d -m 0700 "$state_root"
if test -f "$port_file"; then
  existing=$(cat "$port_file")
  if test "$existing" != "$port"; then
    echo "loki-appliance: MCP port is already configured as $existing" >&2
    exit 1
  fi
else
  tmp=$(mktemp "$state_root/.mcp-port.XXXXXX")
  trap 'rm -f "$tmp"' EXIT HUP INT TERM
  printf '%s\n' "$port" >"$tmp"
  chmod 0600 "$tmp"
  mv -f "$tmp" "$port_file"
  trap - EXIT HUP INT TERM
fi

systemctl reset-failed loki-appliance-provision.service >/dev/null 2>&1 || true
systemctl start --no-block loki-appliance-provision.service
