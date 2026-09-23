#!/bin/sh
set -eu

if test "$#" -ne 1; then
  echo "usage: build-release-inputs.sh OUTPUT_DIRECTORY" >&2
  exit 2
fi

source_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
output=$1
case "$output" in
  /*) ;;
  *) echo "output directory must be absolute" >&2; exit 2 ;;
esac
test ! -e "$output" || { echo "output directory already exists" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
docker buildx version >/dev/null 2>&1 || {
  echo "Docker Buildx with BuildKit is required" >&2
  exit 1
}

parent=$(dirname -- "$output")
test -d "$parent" || { echo "output parent does not exist" >&2; exit 1; }
tmp=$(mktemp -d "$parent/.loki-release-inputs.XXXXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT HUP INT TERM

for arch in amd64 arm64; do
  platform_dir="$tmp/build-$arch"
  docker buildx build "$source_dir" \
    --file "$source_dir/packaging/release-inputs/Dockerfile" \
    --platform "linux/$arch" \
    --provenance=false \
    --output "type=local,dest=$platform_dir"

  for binary in devtools rg gh; do
    test -x "$platform_dir/$binary" || {
      echo "release input build did not produce $binary for linux/$arch" >&2
      exit 1
    }
  done
done

install -d "$tmp/output/devtools/amd64" "$tmp/output/devtools/arm64" \
  "$tmp/output/ripgrep/amd64" "$tmp/output/ripgrep/arm64" \
  "$tmp/output/gh/amd64" "$tmp/output/gh/arm64"

for arch in amd64 arm64; do
  install -m 0755 "$tmp/build-$arch/devtools" "$tmp/output/devtools/$arch/devtools"
  install -m 0755 "$tmp/build-$arch/rg" "$tmp/output/ripgrep/$arch/rg"
  install -m 0755 "$tmp/build-$arch/gh" "$tmp/output/gh/$arch/gh"
done

"$tmp/output/devtools/amd64/devtools" version | grep -q '"version":"0.18.0"'
"$tmp/output/ripgrep/amd64/rg" --version | grep -q '^ripgrep 15\.2\.0$'
"$tmp/output/gh/amd64/gh" version | grep -q '^gh version 2\.101\.0 '

printf '%s\n' \
  "devtools=0.18.0" \
  "ripgrep=15.2.0" \
  "gh=2.101.0" \
  >"$tmp/output/VERSIONS"

mv "$tmp/output" "$output"
