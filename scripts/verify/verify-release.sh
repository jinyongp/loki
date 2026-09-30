#!/bin/sh
set -eu

if test "$#" -ne 3; then
  echo "usage: verify-release.sh CANDIDATE CORE_IMAGE BROWSER_IMAGE" >&2
  exit 2
fi

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
candidate=$(realpath "$1")
core_image=$2
browser_image=$3
docker=${LOKI_DOCKER:-docker}
signing_key=${LOKI_SIGNING_KEY_FILE:-}

fail() {
  printf 'loki validation: profile=exact-candidate error=%s\n' "$*" >&2
  exit 1
}

require_digest() {
  case "$1" in
    *@sha256:????????????????????????????????????????????????????????????????) ;;
    *) fail "$2 image must be pinned by sha256 digest" ;;
  esac
}

sh "$repo/scripts/verify/prepare-candidate.sh" "$candidate" || fail "candidate executable preparation failed"
test -x "$candidate/install.sh" || fail "candidate install script is missing"
test -n "$signing_key" || fail "LOKI_SIGNING_KEY_FILE is required for signing acceptance"
test -f "$signing_key" || fail "signing fixture does not exist: $signing_key"
require_digest "$core_image" core
require_digest "$browser_image" browser

inspect_image() {
  image=$1
  title=$2
  "$docker" pull "$image" >/dev/null
  test "$("$docker" image inspect --format '{{index .Config.Labels "org.opencontainers.image.title"}}' "$image")" = "$title"
  test "$("$docker" image inspect --format '{{.Os}}' "$image")" = linux
  case $("$docker" image inspect --format '{{.Architecture}}' "$image") in amd64|arm64) ;; *) return 1 ;; esac
}

printf 'loki validation: profile=exact-candidate required=source,race,real-oci,authority,bootstrap,candidate,compose-browser-signing\n'
inspect_image "$core_image" Loki
inspect_image "$browser_image" 'Loki Browser'

failures=0
run_check() {
  label=$1
  shift
  printf 'loki validation: profile=exact-candidate check=%s state=running\n' "$label"
  if "$@"; then
    printf 'loki validation: profile=exact-candidate check=%s result=passed\n' "$label"
    return 0
  fi
  failures=$((failures + 1))
  printf 'loki validation: profile=exact-candidate check=%s result=failed\n' "$label" >&2
  return 0
}

run_check source sh "$repo/scripts/verify/verify-source.sh"
run_check race sh "$repo/scripts/verify/verify-race.sh"
run_check real-oci env LOKI_OCI_ACCEPTANCE_IMAGE="$core_image" sh "$repo/scripts/verify/accept-oci-jobs.sh"
run_check authority env LOKI_IMAGE="$core_image" sh "$repo/scripts/verify/accept-authority-matrix.sh"
run_check bootstrap sh "$repo/scripts/verify/accept-bootstrap.sh"
run_check candidate sh "$repo/scripts/verify/accept-candidate.sh" "$candidate"
run_check compose-browser-signing env \
  LOKI_IMAGE="$core_image" \
  LOKI_BROWSER_IMAGE="$browser_image" \
  LOKI_SIGNING_KEY_FILE="$signing_key" \
  sh "$repo/scripts/verify/accept-compose.sh"

if test "$failures" -ne 0; then
  printf 'loki validation: profile=exact-candidate result=failed failures=%s note=all independent exact-candidate domains were attempted\n' "$failures" >&2
  exit 1
fi
printf 'loki validation: profile=exact-candidate result=passed remaining=windows-wsl,publication\n'
