#!/bin/sh
set -eu
umask 077

state_root=/var/lib/loki-appliance
marker=$state_root/provisioned
result=$state_root/install-result.json
host=/usr/lib/loki-appliance/loki
manifest=/usr/lib/loki-appliance/release-manifest.json
workspace=/home/ubuntu/workspace
port_file=$state_root/mcp-port

test ! -e "$marker" || exit 0
test -x "$host"
test -f "$manifest"
test -f "$port_file"
mcp_port=$(cat "$port_file")
case "$mcp_port" in
  ''|*[!0-9]*)
    echo "loki-appliance: invalid MCP port configuration" >&2
    exit 1
    ;;
esac
if test "$mcp_port" -lt 1024 || test "$mcp_port" -gt 65535; then
  echo "loki-appliance: invalid MCP port configuration" >&2
  exit 1
fi
install -d -m 0700 "$state_root"

attempt=0
until docker info >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  test "$attempt" -lt 60 || {
    echo "loki-appliance: Docker did not become ready" >&2
    exit 1
  }
  sleep 1
done

tmp=$(mktemp "$state_root/.install-result.XXXXXX")
cleanup() { rm -f "$tmp"; }
trap cleanup EXIT HUP INT TERM

"$host" host install \
  --system \
  --workspace "$workspace" \
  --mcp-port "$mcp_port" \
  --prepare-workspace \
  --bootstrap-release-manifest "$manifest" \
  --json >"$tmp"

chmod 0600 "$tmp"
mv -f "$tmp" "$result"
trap - EXIT HUP INT TERM

/usr/local/bin/loki host status --system --json >/dev/null
/usr/local/bin/loki host doctor --system >/dev/null

printf '%s\n' provisioned >"$marker"
chmod 0600 "$marker"
