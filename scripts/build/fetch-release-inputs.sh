#!/bin/sh
set -eu

if test "$#" -ne 1; then
  echo "usage: fetch-release-inputs.sh OUTPUT_DIRECTORY" >&2
  exit 2
fi

output=$1
case "$output" in
  /*) ;;
  *) echo "output directory must be absolute" >&2; exit 2 ;;
esac
test ! -e "$output" || { echo "output directory already exists" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo "sha256sum is required" >&2; exit 1; }
command -v tar >/dev/null 2>&1 || { echo "tar is required" >&2; exit 1; }

devtools_version=0.18.0
ripgrep_version=15.2.0
gh_version=2.101.0

tmp=$(mktemp -d "${TMPDIR:-/tmp}/loki-release-inputs.XXXXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT HUP INT TERM

download() {
  repo=$1
  tag=$2
  asset=$3
  curl --fail --location --retry 3 --retry-all-errors \
    --output "$tmp/$asset" \
    "https://github.com/$repo/releases/download/$tag/$asset"
}

for arch in amd64 arm64; do
  asset="devtools_${devtools_version}_linux_${arch}.tar.gz"
  download jinyongp/devtools "v$devtools_version" "$asset"
  download jinyongp/devtools "v$devtools_version" "$asset.sha256"
  (cd "$tmp" && sha256sum -c "$asset.sha256")
done

for tuple in "amd64 x86_64" "arm64 aarch64"; do
  set -- $tuple
  arch=$1
  upstream=$2
  asset="ripgrep-${ripgrep_version}-${upstream}-unknown-linux-musl.tar.gz"
  download BurntSushi/ripgrep "$ripgrep_version" "$asset"
  download BurntSushi/ripgrep "$ripgrep_version" "$asset.sha256"
  (cd "$tmp" && sha256sum -c "$asset.sha256")
done

download cli/cli "v$gh_version" "gh_${gh_version}_checksums.txt"
for arch in amd64 arm64; do
  asset="gh_${gh_version}_linux_${arch}.tar.gz"
  download cli/cli "v$gh_version" "$asset"
  (cd "$tmp" && grep "  $asset\$" "gh_${gh_version}_checksums.txt" | sha256sum -c -)
done

install -d "$output/devtools/amd64" "$output/devtools/arm64" \
  "$output/ripgrep/amd64" "$output/ripgrep/arm64" \
  "$output/gh/amd64" "$output/gh/arm64"

extract_one() {
  archive=$1
  basename=$2
  destination=$3
  directory=$(mktemp -d "$tmp/extract.XXXXXXXX")
  tar -xzf "$tmp/$archive" -C "$directory"
  source=$(find "$directory" -type f -name "$basename" -print | head -n 1)
  test -n "$source" || { echo "archive $archive does not contain $basename" >&2; exit 1; }
  install -m 0755 "$source" "$destination"
}

for arch in amd64 arm64; do
  extract_one "devtools_${devtools_version}_linux_${arch}.tar.gz" devtools "$output/devtools/$arch/devtools"
done
extract_one "ripgrep-${ripgrep_version}-x86_64-unknown-linux-musl.tar.gz" rg "$output/ripgrep/amd64/rg"
extract_one "ripgrep-${ripgrep_version}-aarch64-unknown-linux-musl.tar.gz" rg "$output/ripgrep/arm64/rg"
for arch in amd64 arm64; do
  extract_one "gh_${gh_version}_linux_${arch}.tar.gz" gh "$output/gh/$arch/gh"
done

printf '%s\n' "devtools=$devtools_version" "ripgrep=$ripgrep_version" "gh=$gh_version" >"$output/VERSIONS"
