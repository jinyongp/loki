#!/bin/sh
set -eu

if test "$#" -ne 5; then
  echo "usage: build-loki-oci.sh OUTPUT_OCI DEVTOOLS_AMD64 DEVTOOLS_ARM64 RIPGREP_AMD64 RIPGREP_ARM64" >&2
  exit 2
fi

source_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output=$1
devtools_amd64=$2
devtools_arm64=$3
ripgrep_amd64=$4
ripgrep_arm64=$5

case "$output" in
  /*) ;;
  *) echo "output path must be absolute" >&2; exit 2 ;;
esac
for binary in "$devtools_amd64" "$devtools_arm64"; do
  test -x "$binary" || { echo "devtools binary is not executable: $binary" >&2; exit 1; }
  go version -m "$binary" | grep -q 'path[[:space:]]github.com/jinyongp/devtools/cmd/devtools' || {
    echo "unexpected devtools Go module: $binary" >&2
    exit 1
  }
  go version -m "$binary" | grep -q 'build[[:space:]]GOOS=linux' || {
    echo "devtools binary is not for Linux: $binary" >&2
    exit 1
  }
done
go version -m "$devtools_amd64" | grep -q 'build[[:space:]]GOARCH=amd64' || {
  echo "amd64 devtools input has the wrong architecture" >&2
  exit 1
}
go version -m "$devtools_arm64" | grep -q 'build[[:space:]]GOARCH=arm64' || {
  echo "arm64 devtools input has the wrong architecture" >&2
  exit 1
}

elf_machine() {
  test "$(od -An -tx1 -N4 "$1" | tr -d ' \n')" = 7f454c46 || return 1
  od -An -tu2 -j18 -N2 "$1" | tr -d ' \n'
}
test -x "$ripgrep_amd64" && test "$(elf_machine "$ripgrep_amd64")" = 62 || {
  echo "amd64 ripgrep input has the wrong architecture" >&2
  exit 1
}
test -x "$ripgrep_arm64" && test "$(elf_machine "$ripgrep_arm64")" = 183 || {
  echo "arm64 ripgrep input has the wrong architecture" >&2
  exit 1
}

devtools_version_json=$($devtools_amd64 version)
devtools_version=$(printf '%s\n' "$devtools_version_json" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
test -n "$devtools_version" || { echo "invalid devtools version response" >&2; exit 1; }

amd64_sha=$(sha256sum "$devtools_amd64" | cut -d' ' -f1)
arm64_sha=$(sha256sum "$devtools_arm64" | cut -d' ' -f1)
ripgrep_version=$($ripgrep_amd64 --version | sed -n '1s/^ripgrep //p')
test -n "$ripgrep_version" || { echo "invalid ripgrep version response" >&2; exit 1; }
ripgrep_amd64_sha=$(sha256sum "$ripgrep_amd64" | cut -d' ' -f1)
ripgrep_arm64_sha=$(sha256sum "$ripgrep_arm64" | cut -d' ' -f1)
loki_version=${LOKI_VERSION:-0.48.0-dev}
loki_revision=${LOKI_REVISION:-$(git -C "$source_dir" rev-parse HEAD)}
loki_date=${LOKI_DATE:-$(git -C "$source_dir" show -s --format=%cI "$loki_revision" 2>/dev/null || printf unknown)}
source_date_epoch=${SOURCE_DATE_EPOCH:-$(git -C "$source_dir" show -s --format=%ct "$loki_revision" 2>/dev/null || printf 0)}

artifacts=$(mktemp -d "${TMPDIR:-/tmp}/loki-oci-artifacts.XXXXXX")
cleanup() { rm -rf "$artifacts"; }
trap cleanup EXIT HUP INT TERM
install -d "$artifacts/devtools/amd64" "$artifacts/devtools/arm64" "$artifacts/ripgrep/amd64" "$artifacts/ripgrep/arm64" "$artifacts/metadata"
install -m 0755 "$devtools_amd64" "$artifacts/devtools/amd64/devtools"
install -m 0755 "$devtools_arm64" "$artifacts/devtools/arm64/devtools"
install -m 0755 "$ripgrep_amd64" "$artifacts/ripgrep/amd64/rg"
install -m 0755 "$ripgrep_arm64" "$artifacts/ripgrep/arm64/rg"
go run "$source_dir/internal/devtools/cmd/gencatalog" -binary "$devtools_amd64" -output "$artifacts/metadata/devtools-catalog.json"
printf '{"version":1,"loki":{"version":"%s","revision":"%s","date":"%s"},"devtools":%s,"binaries":{"devtools":{"amd64":{"sha256":"%s"},"arm64":{"sha256":"%s"}},"ripgrep":{"version":"%s","amd64":{"sha256":"%s"},"arm64":{"sha256":"%s"}}}}\n' \
  "$loki_version" "$loki_revision" "$loki_date" "$devtools_version_json" "$amd64_sha" "$arm64_sha" "$ripgrep_version" "$ripgrep_amd64_sha" "$ripgrep_arm64_sha" \
  > "$artifacts/metadata/provenance.json"

docker buildx build "$source_dir" \
  --file "$source_dir/packaging/container/Dockerfile" \
  --platform linux/amd64,linux/arm64 \
  --build-context "artifacts=$artifacts" \
  --build-arg "LOKI_VERSION=$loki_version" \
  --build-arg "LOKI_REVISION=$loki_revision" \
  --build-arg "LOKI_DATE=$loki_date" \
  --build-arg "SOURCE_DATE_EPOCH=$source_date_epoch" \
  --build-arg "DEVTOOLS_VERSION=$devtools_version" \
  --build-arg "DEVTOOLS_AMD64_SHA256=$amd64_sha" \
  --build-arg "DEVTOOLS_ARM64_SHA256=$arm64_sha" \
  --build-arg "RIPGREP_VERSION=$ripgrep_version" \
  --build-arg "RIPGREP_AMD64_SHA256=$ripgrep_amd64_sha" \
  --build-arg "RIPGREP_ARM64_SHA256=$ripgrep_arm64_sha" \
  --provenance=mode=max \
  --output "type=oci,dest=$output"
