#!/bin/sh
set -eu

if test "$#" -ne 3; then
  echo "usage: build-loki-go-candidate.sh OUTPUT_DIRECTORY DEVTOOLS_BINARY TOOLCHAIN_BUNDLE" >&2
  exit 2
fi

SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
OUTPUT=$1
DEVTOOLS=$2
TOOLCHAIN_BUNDLE=$3

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
test -f "$TOOLCHAIN_BUNDLE/manifest.json" -a -f "$TOOLCHAIN_BUNDLE/catalog.json" -a -f "$TOOLCHAIN_BUNDLE/SHA256SUMS" -a -d "$TOOLCHAIN_BUNDLE/artifacts" || {
  echo "verified toolchain bundle is incomplete" >&2
  exit 1
}
cmp "$SOURCE_DIR/packaging/native/toolchain-manifest.json" "$TOOLCHAIN_BUNDLE/manifest.json" || {
  echo "toolchain bundle manifest does not match the candidate source" >&2
  exit 1
}
cmp "$SOURCE_DIR/packaging/native/toolchain-catalog.json" "$TOOLCHAIN_BUNDLE/catalog.json" || {
  echo "toolchain bundle catalog does not match the candidate source" >&2
  exit 1
}
(cd "$TOOLCHAIN_BUNDLE" && sha256sum -c SHA256SUMS)

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
install -d "$ROOT/usr/share/loki/toolchain"

CGO_ENABLED=0 go build -trimpath -o "$ROOT/opt/loki/bin/loki" "$SOURCE_DIR/cmd/loki"
CGO_ENABLED=0 go build -trimpath -o "$ROOT/opt/loki/bin/loki-launcher" "$SOURCE_DIR/cmd/loki-launcher"
CGO_ENABLED=0 go build -trimpath -o "$ROOT/opt/loki/bin/loki-executor" "$SOURCE_DIR/cmd/loki-executor"
install -m 0755 "$DEVTOOLS" "$ROOT/opt/loki/bin/devtools"
install -m 0755 "$SOURCE_DIR/scripts/maintainer/wait-for-loki-sockets.sh" "$ROOT/opt/loki/libexec/wait-for-loki-sockets"
install -m 0755 "$SOURCE_DIR/scripts/maintainer/render-loki-go-layouts.sh" "$ROOT/opt/loki/libexec/render-layouts"
install -m 0755 "$SOURCE_DIR/scripts/maintainer/loki-devtools-launch" "$ROOT/opt/loki/libexec/devtools"
install -m 0755 "$SOURCE_DIR/scripts/maintainer/loki-go-lifecycle.sh" "$ROOT/opt/loki/libexec/lifecycle"
cp -a "$SOURCE_DIR/bundled_skills/." "$ROOT/opt/loki/share/skills/"
cp -a "$SOURCE_DIR/bundled_skills/." "$ROOT/srv/workspace/loki/.agents/skills/"
install -m 0644 "$SOURCE_DIR/config/loki-go.toml" "$ROOT/usr/share/doc/loki/config.toml"
install -m 0644 "$SOURCE_DIR/config/loki-gitconfig" "$ROOT/usr/share/doc/loki/gitconfig"
install -m 0644 "$SOURCE_DIR/packaging/native/runtime.json.in" "$ROOT/usr/share/doc/loki/runtime.json.in"
install -m 0644 "$SOURCE_DIR/packaging/native/mcp.json.in" "$ROOT/usr/share/doc/loki/mcp.json.in"
install -m 0644 "$SOURCE_DIR/packaging/native/launcher.json.in" "$ROOT/usr/share/doc/loki/launcher.json.in"
install -m 0644 "$SOURCE_DIR/packaging/native/executor.json.in" "$ROOT/usr/share/doc/loki/executor.json.in"
install -m 0644 "$SOURCE_DIR/packaging/native/execution-contract.json" "$ROOT/usr/share/doc/loki/execution-contract.json"
install -m 0644 "$SOURCE_DIR/packaging/native/egress-policy.json" "$ROOT/usr/share/doc/loki/egress-policy.json"
install -m 0644 "$SOURCE_DIR/packaging/native/toolchain-manifest.json" "$ROOT/usr/share/doc/loki/toolchain-manifest.json"
install -m 0644 "$SOURCE_DIR/packaging/native/toolchain-catalog.json" "$ROOT/usr/share/doc/loki/toolchain-catalog.json"
install -m 0644 "$CATALOG" "$ROOT/usr/share/doc/loki/devtools-catalog.json"
cp -a "$TOOLCHAIN_BUNDLE/." "$ROOT/usr/share/loki/toolchain/"
install -m 0644 "$SOURCE_DIR/packaging/native/systemd/"*.service "$SOURCE_DIR/packaging/native/systemd/"*.target "$ROOT/usr/lib/systemd/system/"
install -m 0644 "$SOURCE_DIR/packaging/native/tmpfiles.d/loki-go.conf" "$ROOT/usr/lib/tmpfiles.d/loki-go.conf"
ln -s ../../../opt/loki/bin/loki "$ROOT/usr/local/bin/loki"
ln -s ../../../opt/loki/libexec/devtools "$ROOT/usr/local/bin/devtools"

(
  cd "$ROOT"
  find . -type f -print0 | sort -z | xargs -0 sha256sum
) > "$OUTPUT/SHA256SUMS"

printf '%s\n' "loki=$("$ROOT/opt/loki/bin/loki" version)" > "$OUTPUT/VERSIONS"
printf '%s\n' "devtools=$VERSION" >> "$OUTPUT/VERSIONS"
install -m 0755 "$SOURCE_DIR/scripts/maintainer/stage-loki-go-candidate.sh" "$OUTPUT/stage.sh"
install -m 0755 "$SOURCE_DIR/scripts/maintainer/loki-go-lifecycle.sh" "$OUTPUT/install.sh"
