#!/bin/sh
set -eu

if test "$#" -ne 2; then
  echo "usage: build-release-inputs.sh OUTPUT_DIRECTORY ARCH" >&2
  exit 2
fi

source_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
output=$1
arch=$2
case "$output" in
  /*) ;;
  *) echo "output directory must be absolute" >&2; exit 2 ;;
esac
case "$arch" in
  amd64|arm64) ;;
  *) echo "architecture must be amd64 or arm64" >&2; exit 2 ;;
esac
test ! -e "$output" || { echo "output directory already exists" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
docker buildx version >/dev/null 2>&1 || {
  echo "Docker Buildx with BuildKit is required" >&2
  exit 1
}

case "$(uname -m)" in
  x86_64) host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  *) echo "unsupported release-input build host architecture" >&2; exit 1 ;;
esac
test "$host_arch" = "$arch" || {
  echo "release input architecture $arch requires a native $arch runner" >&2
  exit 1
}

parent=$(dirname -- "$output")
test -d "$parent" || { echo "output parent does not exist" >&2; exit 1; }
tmp=$(mktemp -d "$parent/.loki-release-inputs.XXXXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT HUP INT TERM

build_target() {
  target=$1
  destination=$2
  log="$tmp/$target-$arch.log"
  set -- docker buildx build "$source_dir" \
    --file "$source_dir/packaging/release-inputs/Dockerfile" \
    --platform "linux/$arch" \
    --target "$target" \
    --progress=plain \
    --provenance=false \
    --output "type=local,dest=$destination"
  if test -n "${LOKI_BUILD_CACHE_SCOPE:-}"; then
    scope="${LOKI_BUILD_CACHE_SCOPE}-$target-$arch"
    set -- "$@" \
      --cache-from "type=gha,version=2,scope=$scope" \
      --cache-to "type=gha,version=2,mode=max,scope=$scope,ignore-error=true"
  fi
  if ! "$@" >"$log" 2>&1; then
    tail -n 40 "$log" >&2 || true
    summary=$(tail -n 1 "$log" | tr '\r\n' '  ' | sed 's/%/%25/g; s/::/%3A%3A/g')
    printf '::error::release input %s linux/%s source build failed: %s\n' "$target" "$arch" "$summary" >&2
    return 1
  fi
}

go_dir="$tmp/go-$arch"
ripgrep_dir="$tmp/ripgrep-$arch"
build_target go-export "$go_dir"
build_target ripgrep-export "$ripgrep_dir"

for binary in devtools gh; do
  test -x "$go_dir/$binary" || {
    echo "::error title=release input go-export linux/$arch::missing $binary output" >&2
    exit 1
  }
done
test -x "$ripgrep_dir/rg" || {
  echo "::error title=release input ripgrep-export linux/$arch::missing rg output" >&2
  exit 1
}

install -d "$tmp/output/devtools/$arch" "$tmp/output/ripgrep/$arch" "$tmp/output/gh/$arch"
install -m 0755 "$go_dir/devtools" "$tmp/output/devtools/$arch/devtools"
install -m 0755 "$ripgrep_dir/rg" "$tmp/output/ripgrep/$arch/rg"
install -m 0755 "$go_dir/gh" "$tmp/output/gh/$arch/gh"

printf '%s\n' \
  "arch=$arch" \
  "devtools=0.18.0" \
  "ripgrep=15.2.0" \
  "gh=2.101.0" \
  >"$tmp/output/VERSIONS-$arch"

mv "$tmp/output" "$output"
