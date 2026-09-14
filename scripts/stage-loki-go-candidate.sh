#!/bin/sh
set -eu

if test "$#" -ne 5; then
  echo "usage: stage.sh NEW_ROOT RUNNER_UID RUNNER_GID WORKSPACE_GID BROWSER_UID" >&2
  exit 2
fi

ARTIFACT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TARGET=$1

case "$TARGET" in
  /*) ;;
  *) echo "target root must be absolute" >&2; exit 2 ;;
esac
test ! -e "$TARGET" || {
  echo "target root already exists" >&2
  exit 1
}
test -d "$ARTIFACT/rootfs"
test -f "$ARTIFACT/SHA256SUMS"

(
  cd "$ARTIFACT/rootfs"
  sha256sum -c ../SHA256SUMS
)

mkdir -m 0750 "$TARGET"
cp -a "$ARTIFACT/rootfs/." "$TARGET/"
install -d -m 0750 "$TARGET/etc/loki-go"
install -d -m 0700 "$TARGET/var/lib/loki-go/runtime/inbox"
install -d -m 0700 "$TARGET/var/lib/loki-go/devtools/snapshots"
install -d -m 0700 "$TARGET/var/lib/loki-go/signing"
install -d -m 0700 "$TARGET/var/lib/loki-go/browser"
install -d -m 0700 "$TARGET/var/log/loki-go/runtime"
install -d -m 0700 "$TARGET/var/log/loki-go/mcp"
install -d -m 0770 "$TARGET/srv/workspace/loki/.loki-go/browser-downloads"

install -m 0640 "$TARGET/usr/share/doc/loki/config.toml" "$TARGET/etc/loki-go/config.toml"
install -m 0644 "$TARGET/usr/share/doc/loki/gitconfig" "$TARGET/etc/loki-go/gitconfig"
"$TARGET/opt/loki/libexec/render-layouts" "$TARGET/usr/share/doc/loki" "$TARGET/etc/loki-go" "$2" "$3" "$4" "$5"

umask 0077
dd if=/dev/urandom bs=48 count=1 2>/dev/null | base64 > "$TARGET/etc/loki-go/token"
ssh-keygen -q -t ed25519 -N "" -C "loki-go candidate signing" -f "$TARGET/var/lib/loki-go/signing/id_ed25519"
