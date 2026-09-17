#!/bin/sh
set -eu

REPO_DIR=${1:-$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)}
REPO_DIR=$(CDPATH= cd -- "$REPO_DIR" && pwd)
SOURCE_DIR="$REPO_DIR/legacy/python"

test "$(id -u)" -eq 0
test -f "$SOURCE_DIR/systemd/loki-cloudflared.service"

install -d -o root -g root -m 0755 /usr/share/keyrings
key_file=$(mktemp /tmp/cloudflare-main.gpg.XXXXXX)
trap 'rm -f "$key_file"' EXIT HUP INT TERM
curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg -o "$key_file"
install -o root -g root -m 0644 "$key_file" /usr/share/keyrings/cloudflare-main.gpg

printf '%s\n' 'deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main' \
  > /etc/apt/sources.list.d/cloudflared.list
chmod 0644 /etc/apt/sources.list.d/cloudflared.list

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y cloudflared

if ! getent passwd loki-tunnel >/dev/null; then
  useradd --system --user-group --create-home --home-dir /var/lib/loki/cloudflared --shell /usr/sbin/nologin loki-tunnel
fi
install -d -o root -g loki-tunnel -m 0750 /etc/loki/cloudflared
install -d -o loki-tunnel -g loki-tunnel -m 0700 /var/log/loki/cloudflared
install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-cloudflared.service" /etc/systemd/system/loki-cloudflared.service
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/configure-cloudflared-token" /usr/local/sbin/loki-cloudflare-configure

systemctl daemon-reload
echo 'cloudflared installed. Run /usr/local/sbin/loki-cloudflare-configure as root to connect the tunnel.'
