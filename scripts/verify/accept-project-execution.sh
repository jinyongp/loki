#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

image=${1:?usage: accept-project-execution.sh CORE_IMAGE TOOLCHAIN_BUNDLE}
bundle=${2:?usage: accept-project-execution.sh CORE_IMAGE TOOLCHAIN_BUNDLE}
docker=${LOKI_DOCKER:-docker}
root=$(mktemp -d "${TMPDIR:-/tmp}/loki-project-execution.XXXXXX")
container=

cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if test -n "$container"; then
    "$docker" rm -f "$container" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$root"
  exit "$result"
}
trap cleanup EXIT HUP INT TERM

fail() {
  printf 'loki project execution acceptance: %s\n' "$*" >&2
  exit 1
}

case "$image" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) fail "CORE_IMAGE must be pinned by a sha256 digest" ;;
esac
test -d "$bundle" || fail "toolchain bundle does not exist: $bundle"
for command in "$docker" go jq sha256sum bsdtar; do
  command -v "$command" >/dev/null 2>&1 || fail "required command is missing: $command"
done
(
  cd "$bundle"
  sha256sum -c SHA256SUMS >/dev/null
)

manifest="$bundle/manifest.json"
test -f "$manifest" || fail "toolchain bundle manifest is missing"

extract_artifact() {
  name=$1
  destination=$2
  filename=$(jq -er --arg name "$name" '.artifacts[] | select(.name == $name) | .filename' "$manifest")
  strip=$(jq -er --arg name "$name" '.artifacts[] | select(.name == $name) | .strip_components' "$manifest")
  archive="$bundle/artifacts/$filename"
  test -f "$archive" || fail "$name artifact is missing from the canonical bundle"
  mkdir -p "$destination"
  bsdtar -xf "$archive" -C "$destination" --strip-components "$strip"
}

extract_artifact node "$root/node"
extract_artifact pnpm "$root/pnpm"
extract_artifact chromium "$root/chromium"

node_link=$(jq -er '.artifacts[] | select(.name == "node") | .links.node' "$manifest")
pnpm_link=$(jq -er '.artifacts[] | select(.name == "pnpm") | .links.pnpm' "$manifest")
chromium_link=$(jq -er '.artifacts[] | select(.name == "chromium") | .links.chromium' "$manifest")
node="$root/node/$node_link"
pnpm="$root/pnpm/$pnpm_link"
chromium="$root/chromium/$chromium_link"
for executable in "$node" "$pnpm" "$chromium"; do
  test -x "$executable" || fail "candidate toolchain executable is not executable: $executable"
done

devtools="$root/devtools"
"$docker" pull "$image" >/dev/null
container=$("$docker" create "$image")
"$docker" cp "$container:/opt/loki/bin/devtools" "$devtools"
"$docker" rm -f "$container" >/dev/null
container=
chmod 0755 "$devtools"

case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported acceptance architecture: $(uname -m)" ;;
esac
label="io.loki.devtools.$arch.sha256"
expected_devtools=$("$docker" image inspect --format "{{ index .Config.Labels \"$label\" }}" "$image")
actual_devtools=$(sha256sum "$devtools" | awk '{print $1}')
test "$actual_devtools" = "$expected_devtools" ||
  fail "release-image devtools digest does not match immutable image metadata"

LOKI_E2E_DEVTOOLS="$devtools" \
LOKI_E2E_PNPM="$pnpm" \
LOKI_E2E_NODE="$node" \
LOKI_E2E_CHROMIUM="$chromium" \
  go test ./internal/e2e -run '^TestProjectExecutionContract$' -v -count=1

printf 'loki project execution acceptance: passed exact release-image devtools with canonical pinned node/pnpm/chromium\n'
