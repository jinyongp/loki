#!/bin/sh
set -eu
umask 077

state_root=/var/lib/loki-appliance
marker=$state_root/provisioned
result=$state_root/install-result.json
host=/usr/lib/loki-appliance/loki
manifest=/usr/lib/loki-appliance/release-manifest.json
workspace=/home/ubuntu/workspace

test ! -e "$marker" || exit 0
test -x "$host"
test -f "$manifest"
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
