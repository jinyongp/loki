#!/bin/sh
set -eu

if test "$#" -ne 7; then
  echo "usage: stage.sh NEW_ROOT RUNNER_UID RUNNER_GID WORKSPACE_GID BROWSER_UID EXECUTOR_UID JOB_IMAGE" >&2
  exit 2
fi

ARTIFACT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TARGET=$1
RUNNER_UID=$2
RUNNER_GID=$3
WORKSPACE_GID=$4
BROWSER_UID=$5
EXECUTOR_UID=$6
JOB_IMAGE=$7

case "$TARGET" in
  /*) ;;
  *) echo "target root must be absolute" >&2; exit 2 ;;
esac
for identity in "$RUNNER_UID" "$RUNNER_GID" "$WORKSPACE_GID" "$BROWSER_UID" "$EXECUTOR_UID"; do
  case "$identity" in
    *[!0-9]*|'') echo "service identities must be numeric" >&2; exit 2 ;;
  esac
done
test "$EXECUTOR_UID" -gt 0
test "$EXECUTOR_UID" -ne "$RUNNER_UID"
test "$EXECUTOR_UID" -ne "$BROWSER_UID"
printf '%s\n' "$JOB_IMAGE" | grep -Eq '^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]{1,5})?(/[a-z0-9]+([._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}$'
test ! -e "$TARGET" || {
  echo "target root already exists" >&2
  exit 1
}
test -d "$ARTIFACT/rootfs"
test -f "$ARTIFACT/SHA256SUMS"
test "$(id -u)" -eq 0 || {
  echo "stage must run as root to assign service ownership" >&2
  exit 1
}

(
  cd "$ARTIFACT/rootfs"
  sha256sum -c ../SHA256SUMS
)

mkdir -m 0750 "$TARGET"
cp -a --no-preserve=ownership "$ARTIFACT/rootfs/." "$TARGET/"
"$TARGET/opt/loki/bin/loki" toolchain install \
  --bundle "$TARGET/usr/share/loki/toolchain" \
  --root "$TARGET" \
  --skip-apt
install -d -g "$WORKSPACE_GID" -m 0750 "$TARGET/etc/loki-go"
install -d -m 0700 "$TARGET/var/lib/loki-go/runtime/inbox"
install -d -m 0700 "$TARGET/var/lib/loki-go/signing"
install -d -m 0700 "$TARGET/var/lib/loki-go/browser"
install -d -m 0700 "$TARGET/var/lib/loki-go/launcher"
install -d -m 0700 "$TARGET/var/log/loki-go/runtime"
install -d -m 0700 "$TARGET/var/log/loki-go/mcp"
install -d -m 0700 "$TARGET/var/log/loki-go/launcher"
install -d -m 0700 "$TARGET/var/log/loki-go/executor"
install -d -o "$RUNNER_UID" -g "$WORKSPACE_GID" -m 2770 "$TARGET/srv/workspace/loki"
install -d -o "$BROWSER_UID" -g "$WORKSPACE_GID" -m 0770 "$TARGET/srv/workspace/loki/.loki-go/browser-downloads"
install -d -o "$RUNNER_UID" -g "$RUNNER_GID" -m 0700 \
  "$TARGET/var/lib/loki-go/runner" \
  "$TARGET/var/lib/loki-go/runner-config" \
  "$TARGET/var/lib/loki-go/runner-gh-config" \
  "$TARGET/var/lib/loki-go/runner-data" \
  "$TARGET/var/lib/loki-go/runner-xdg-state" \
  "$TARGET/var/lib/loki-go/snapshots" \
  "$TARGET/var/cache/loki-go/runner" \
  "$TARGET/var/cache/loki-go/runner-npm" \
  "$TARGET/var/cache/loki-go/runner-pnpm" \
  "$TARGET/var/cache/loki-go/runner-playwright" \
  "$TARGET/var/cache/loki-go/runner-go-build" \
  "$TARGET/var/cache/loki-go/runner-go-mod" \
  "$TARGET/var/cache/loki-go/runner-pip" \
  "$TARGET/var/tmp/loki-go/runner"

install -m 0640 "$TARGET/usr/share/doc/loki/config.toml" "$TARGET/etc/loki-go/config.toml"
install -m 0644 "$TARGET/usr/share/doc/loki/gitconfig" "$TARGET/etc/loki-go/gitconfig"
chgrp "$WORKSPACE_GID" "$TARGET/etc/loki-go/config.toml"
"$TARGET/opt/loki/libexec/render-layouts" "$TARGET/usr/share/doc/loki" "$TARGET/etc/loki-go" \
  "$RUNNER_UID" "$RUNNER_GID" "$WORKSPACE_GID" "$BROWSER_UID" "$EXECUTOR_UID" "$JOB_IMAGE"

umask 0077
dd if=/dev/urandom bs=48 count=1 2>/dev/null | base64 > "$TARGET/etc/loki-go/token"
chgrp "$WORKSPACE_GID" "$TARGET/etc/loki-go/token"
chmod 0640 "$TARGET/etc/loki-go/token"
ssh-keygen -q -t ed25519 -N "" -C "loki-go candidate signing" -f "$TARGET/var/lib/loki-go/signing/id_ed25519"
