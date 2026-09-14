#!/bin/sh
set -eu

if test "$#" -ne 2; then
  echo "usage: build-loki-go-candidate.sh OUTPUT_DIRECTORY DEVTOOLS_BINARY" >&2
  exit 2
fi

SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUTPUT=$1
DEVTOOLS=$2

case "$OUTPUT" in
  /*) ;;
  *) echo "output directory must be absolute" >&2; exit 2 ;;
esac
test ! -e "$OUTPUT" || {
  echo "output directory already exists" >&2
  exit 1
}
test -x "$DEVTOOLS" || {
  echo "devtools binary is not executable" >&2
  exit 1
}

VERSION=$("$DEVTOOLS" version)
CATALOG=$(mktemp /tmp/loki-devtools-catalog.XXXXXX)
trap 'rm -f "$CATALOG"' EXIT HUP INT TERM
go run "$SOURCE_DIR/internal/devtools/cmd/gencatalog" -binary "$DEVTOOLS" -output "$CATALOG"

ROOT="$OUTPUT/rootfs"
install -d "$ROOT/opt/loki/bin" "$ROOT/opt/loki/libexec" "$ROOT/opt/loki/share/skills"
install -d "$ROOT/usr/lib/systemd/system" "$ROOT/usr/share/doc/loki"
install -d "$ROOT/usr/lib/tmpfiles.d"
install -d "$ROOT/usr/local/bin" "$ROOT/srv/workspace/loki/.agents/skills"
install -d "$ROOT/etc/loki-go"

CGO_ENABLED=0 go build -trimpath -o "$ROOT/opt/loki/bin/loki" "$SOURCE_DIR/cmd/loki"
install -m 0755 "$DEVTOOLS" "$ROOT/opt/loki/bin/devtools"
install -m 0755 "$SOURCE_DIR/scripts/wait-for-loki-sockets.sh" "$ROOT/opt/loki/libexec/wait-for-loki-sockets"
install -m 0755 "$SOURCE_DIR/scripts/render-loki-go-layouts.sh" "$ROOT/opt/loki/libexec/render-layouts"
install -m 0755 "$SOURCE_DIR/scripts/loki-devtools-launch" "$ROOT/opt/loki/libexec/devtools"
cp -a "$SOURCE_DIR/bundled_skills/." "$ROOT/opt/loki/share/skills/"
cp -a "$SOURCE_DIR/bundled_skills/." "$ROOT/srv/workspace/loki/.agents/skills/"
install -m 0644 "$SOURCE_DIR/config/loki-go.toml" "$ROOT/usr/share/doc/loki/config.toml"
install -m 0644 "$SOURCE_DIR/config/loki-gitconfig" "$ROOT/usr/share/doc/loki/gitconfig"
install -m 0644 "$SOURCE_DIR/packaging/go/runtime.json.in" "$ROOT/usr/share/doc/loki/runtime.json.in"
install -m 0644 "$SOURCE_DIR/packaging/go/mcp.json.in" "$ROOT/usr/share/doc/loki/mcp.json.in"
install -m 0644 "$SOURCE_DIR/packaging/go/execution-contract.json" "$ROOT/usr/share/doc/loki/execution-contract.json"
install -m 0644 "$SOURCE_DIR/packaging/go/systemd/"*.service "$ROOT/usr/lib/systemd/system/"
install -m 0644 "$SOURCE_DIR/packaging/go/tmpfiles.d/loki-go.conf" "$ROOT/usr/lib/tmpfiles.d/loki-go.conf"
ln -s ../../../opt/loki/bin/loki "$ROOT/usr/local/bin/loki"
ln -s ../../../opt/loki/libexec/devtools "$ROOT/usr/local/bin/devtools"

(
  cd "$ROOT"
  find . -type f -print0 | sort -z | xargs -0 sha256sum
) > "$OUTPUT/SHA256SUMS"

printf '%s\n' "loki=$("$ROOT/opt/loki/bin/loki" version)" > "$OUTPUT/VERSIONS"
printf '%s\n' "devtools=$VERSION" >> "$OUTPUT/VERSIONS"
install -m 0755 "$SOURCE_DIR/scripts/stage-loki-go-candidate.sh" "$OUTPUT/stage.sh"
