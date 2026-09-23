#!/bin/sh
set -eu

source_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
mode=${1:-metadata}

fail() {
  printf '::error title=stale release dependency::%s\n' "$*" >&2
  exit 1
}

require() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required for release pin verification"
}

extract_arg() {
  file=$1
  name=$2
  sed -n "s/^ARG $name=//p" "$file" | head -n 1
}

verify_metadata() {
  for tool in curl jq gh npm awk sed grep sort tar; do
    require "$tool"
  done

  go_pinned=$(awk '$1 == "go" { print $2; exit }' "$source_dir/go.mod")
  go_latest=$(curl -fsSL 'https://go.dev/dl/?mode=json' |
    jq -er '[.[] | select(.stable == true)][0].version | sub("^go"; "")')
  test "$go_pinned" = "$go_latest" ||
    fail "Go $go_pinned is stale; latest stable is $go_latest"

  rust_pinned=$(sed -n 's/.*rust:\([0-9][0-9.]*\)-alpine.*/\1/p'     "$source_dir/packaging/release-inputs/Dockerfile" | head -n 1)
  rust_latest=$(curl -fsSL 'https://static.rust-lang.org/dist/channel-rust-stable.toml' |
    awk '/^\[pkg\.rust\]$/ { found=1; next } found && /^version = / { gsub(/"/, "", $3); print $3; exit }')
  test -n "$rust_pinned" && test "$rust_pinned" = "$rust_latest" ||
    fail "Rust $rust_pinned is stale; latest stable is $rust_latest"

  alpine_pinned=$(sed -n 's/.*alpine:\([0-9][0-9.]*\)@sha256:.*/\1/p'     "$source_dir/packaging/images/Dockerfile"     "$source_dir/packaging/images/browser.Dockerfile" | sort -u)
  alpine_latest=$(curl -fsSL 'https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/x86_64/' |
    grep -o 'alpine-minirootfs-[0-9][0-9.]*-x86_64\.tar\.gz' |
    sed 's/^alpine-minirootfs-//; s/-x86_64\.tar\.gz$//' |
    sort -V | tail -n 1)
  test "$(printf '%s\n' "$alpine_pinned" | wc -l)" -eq 1 &&
    test "$alpine_pinned" = "$alpine_latest" ||
    fail "Alpine pin $alpine_pinned is stale or inconsistent; latest stable is $alpine_latest"

  alpine_branch=$(printf '%s\n' "$alpine_latest" | awk -F. '{ print $1 "." $2 }')
  chromium_pinned=$(extract_arg "$source_dir/packaging/images/browser.Dockerfile" CHROMIUM_VERSION)
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/loki-release-pins.XXXXXXXX")
  trap 'rm -rf "$tmp"' EXIT HUP INT TERM
  for arch in x86_64 aarch64; do
    curl -fsSL       "https://dl-cdn.alpinelinux.org/alpine/v$alpine_branch/community/$arch/APKINDEX.tar.gz"       -o "$tmp/APKINDEX-$arch.tar.gz"
    chromium_latest=$(tar -xOzf "$tmp/APKINDEX-$arch.tar.gz" APKINDEX |
      awk 'BEGIN { RS=""; FS="\n" } {
        package=""; version="";
        for (i=1; i<=NF; i++) {
          if ($i == "P:chromium") package="chromium";
          if ($i ~ /^V:/) version=substr($i, 3);
        }
        if (package == "chromium") { print version; exit }
      }')
    test "$chromium_pinned" = "$chromium_latest" ||
      fail "Chromium $chromium_pinned is stale for Alpine v$alpine_branch/$arch; latest is $chromium_latest"
  done

  devtools_pinned=$(extract_arg "$source_dir/packaging/release-inputs/Dockerfile" DEVTOOLS_VERSION)
  gh_pinned=$(extract_arg "$source_dir/packaging/release-inputs/Dockerfile" GH_VERSION)
  ripgrep_pinned=$(extract_arg "$source_dir/packaging/release-inputs/Dockerfile" RIPGREP_VERSION)
  devtools_latest=$(gh release view --repo jinyongp/devtools --json tagName --jq .tagName)
  gh_latest=$(gh release view --repo cli/cli --json tagName --jq .tagName)
  ripgrep_latest=$(gh release view --repo BurntSushi/ripgrep --json tagName --jq .tagName)
  test "v$devtools_pinned" = "$devtools_latest" ||
    fail "devtools $devtools_pinned is stale; latest is $devtools_latest"
  test "v$gh_pinned" = "$gh_latest" ||
    fail "GitHub CLI $gh_pinned is stale; latest is $gh_latest"
  test "$ripgrep_pinned" = "$ripgrep_latest" ||
    fail "ripgrep $ripgrep_pinned is stale; latest is $ripgrep_latest"

  releaseway_pinned=$(sed -n     's/.*releaseway\/actions@[0-9a-f][0-9a-f]* # \(v[0-9][0-9.]*\)$/\1/p'     "$source_dir/.github/workflows/release.yml" | head -n 1)
  releaseway_latest=$(gh release view --repo releaseway/actions --json tagName --jq .tagName)
  test -n "$releaseway_pinned" && test "$releaseway_pinned" = "$releaseway_latest" ||
    fail "releaseway/actions $releaseway_pinned is stale; latest is $releaseway_latest"
}

inspect_digest() {
  docker buildx imagetools inspect "$1" |
    awk '$1 == "Digest:" { print $2; exit }'
}

verify_ref() {
  pinned=$1
  floating=$2
  label=$3
  pinned_digest=${pinned##*@}
  current_digest=$(inspect_digest "$floating")
  test -n "$current_digest" && test "$pinned_digest" = "$current_digest" ||
    fail "$label digest is stale: pinned $pinned_digest, current $current_digest ($floating)"
}

verify_frontend() {
  file=$1
  label=$2
  frontend=$(sed -n '1s/^# syntax=//p' "$file")
  test -n "$frontend" || fail "$label Dockerfile frontend pin is missing"
  verify_ref "$frontend" docker/dockerfile:1 "$label Dockerfile frontend"
}

verify_go_builder() {
  file=$1
  label=$2
  golang=$(sed -n 's/^FROM --platform=\$BUILDPLATFORM \(golang:[^ ]*@sha256:[0-9a-f]*\) AS .*/\1/p' "$file" | head -n 1)
  test -n "$golang" || fail "$label Go builder image pin is missing"
  verify_ref "$golang" "${golang%@*}" "$label Go builder image"
}

verify_alpine_runtime() {
  file=$1
  label=$2
  alpine=$(sed -n 's/^FROM \(alpine:[^ ]*@sha256:[0-9a-f]*\)$/\1/p' "$file" | tail -n 1)
  test -n "$alpine" || fail "$label Alpine runtime image pin is missing"
  verify_ref "$alpine" "${alpine%@*}" "$label Alpine runtime image"
}

verify_containers() {
  require docker
  docker buildx version >/dev/null 2>&1 ||
    fail "Docker Buildx is required for container pin verification"

  for item in \
    "$source_dir/packaging/images/Dockerfile|core" \
    "$source_dir/packaging/images/browser.Dockerfile|browser" \
    "$source_dir/packaging/release-inputs/Dockerfile|release-inputs"
  do
    file=${item%%|*}
    label=${item#*|}
    verify_frontend "$file" "$label"
    verify_go_builder "$file" "$label"
  done

  verify_alpine_runtime "$source_dir/packaging/images/Dockerfile" core
  verify_alpine_runtime "$source_dir/packaging/images/browser.Dockerfile" browser

  git_image=$(sed -n \
    's/^FROM \(alpine\/git:[^ ]*@sha256:[0-9a-f]*\) AS git-root$/\1/p' \
    "$source_dir/packaging/images/Dockerfile")
  test -n "$git_image" || fail "alpine/git image pin is missing"
  verify_ref "$git_image" alpine/git:latest "alpine/git image"

  rust=$(sed -n \
    's/^FROM --platform=\$TARGETPLATFORM \(rust:[^ ]*@sha256:[0-9a-f]*\) AS ripgrep$/\1/p' \
    "$source_dir/packaging/release-inputs/Dockerfile")
  test -n "$rust" || fail "Rust builder image pin is missing"
  verify_ref "$rust" "${rust%@*}" "Rust builder image"
}

case "$mode" in
  metadata) verify_metadata ;;
  containers) verify_containers ;;
  *) echo "usage: release-pins.sh [metadata|containers]" >&2; exit 2 ;;
esac
