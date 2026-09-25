#!/bin/sh
set -eu
umask 077

if test "$#" -ne 2; then
  echo "usage: build-wsl.sh OUTPUT_WSL RELEASE_DIRECTORY" >&2
  exit 2
fi

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
output=$1
release=$2

case "$output" in
  /*.wsl) ;;
  *) echo "output must be an absolute .wsl path" >&2; exit 2 ;;
esac
case "$release" in
  /*) ;;
  *) echo "release directory must be absolute" >&2; exit 2 ;;
esac

test ! -e "$output" || {
  echo "WSL output already exists" >&2
  exit 1
}
test -d "$release" || {
  echo "release directory does not exist" >&2
  exit 1
}
host=$release/loki-linux-amd64
manifest=$release/release-manifest.json
test -x "$host" || {
  echo "release host binary is missing or not executable" >&2
  exit 1
}
test -f "$manifest" || {
  echo "release manifest is missing" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || {
  echo "Docker is required to build the WSL appliance" >&2
  exit 1
}
docker buildx version >/dev/null 2>&1 || {
  echo "Docker Buildx with BuildKit is required" >&2
  exit 1
}
command -v jq >/dev/null 2>&1 || {
  echo "jq is required to validate the release manifest" >&2
  exit 1
}
command -v gzip >/dev/null 2>&1 || {
  echo "gzip is required to package the WSL appliance" >&2
  exit 1
}

expected_sha=$(jq -er '.host_binary.sha256 | select(test("^[0-9a-f]{64}$"))' "$manifest")
expected_length=$(jq -er '.host_binary.length | select(type == "number" and . > 0)' "$manifest")
actual_sha=$(sha256sum "$host" | awk '{print $1}')
actual_length=$(wc -c <"$host" | tr -d ' ')
test "$actual_sha" = "$expected_sha" || {
  echo "release host binary SHA-256 does not match the manifest" >&2
  exit 1
}
test "$actual_length" = "$expected_length" || {
  echo "release host binary length does not match the manifest" >&2
  exit 1
}

parent=$(dirname -- "$output")
test -d "$parent" || {
  echo "output parent does not exist" >&2
  exit 1
}
raw=$(mktemp "$parent/.loki-wsl.XXXXXXXX.tar")
packed=$(mktemp "$parent/.loki-wsl.XXXXXXXX.wsl")
cleanup() { rm -f "$raw" "$packed"; }
trap cleanup EXIT HUP INT TERM

source_date_epoch=${SOURCE_DATE_EPOCH:-0}
docker buildx build \
  --platform linux/amd64 \
  --build-arg "SOURCE_DATE_EPOCH=$source_date_epoch" \
  --build-context "release=$release" \
  --file "$repo/packaging/wsl/Dockerfile" \
  --output "type=tar,dest=$raw" \
  "$repo"

resolv_entry=$(tar -tf "$raw" | grep -E '^\./?etc/resolv\.conf$' | head -n 1 || true)
test -n "$resolv_entry" || {
  echo "WSL rootfs export did not contain the expected BuildKit resolv.conf mount" >&2
  exit 1
}
tar --delete --file="$raw" "$resolv_entry"
if tar -tf "$raw" | sed 's#^\./##' | grep -Fxq etc/resolv.conf; then
  echo "WSL rootfs still contains /etc/resolv.conf after export cleanup" >&2
  exit 1
fi

gzip -n -9 <"$raw" >"$packed"
chmod 0644 "$packed"
sh "$repo/scripts/verify/verify-wsl.sh" "$packed" "$host" "$manifest"
mv -- "$packed" "$output"
rm -f "$raw"
trap - EXIT HUP INT TERM
