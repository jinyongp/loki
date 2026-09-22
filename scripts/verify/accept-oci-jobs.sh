#!/bin/sh
set -eu

if test "$#" -ne 0; then
  echo "usage: accept-oci-jobs.sh" >&2
  exit 2
fi

SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)

fail() {
  echo "loki OCI Job acceptance: $*" >&2
  exit 1
}

test "$(uname -s)" = Linux || fail "requires a Linux host"
command -v docker >/dev/null 2>&1 || fail "docker CLI is required"
command -v go >/dev/null 2>&1 || fail "Go toolchain is required"

socket=${LOKI_TEST_DOCKER_SOCKET:-/var/run/docker.sock}
case "$socket" in
  /*) ;;
  *) fail "Docker socket must be an absolute path" ;;
esac
test -S "$socket" || fail "Docker socket is not a Unix socket: $socket"

docker_host="unix://$socket"
docker_cmd() {
  docker --host "$docker_host" "$@"
}

docker_cmd info >/dev/null 2>&1 || fail "cannot reach Docker daemon through $socket"
docker_cmd buildx version >/dev/null 2>&1 || fail "Docker Buildx with BuildKit is required; install the current Docker Buildx plugin instead of using the legacy builder"

peer_uid=${LOKI_TEST_DOCKER_PEER_UID:-$(stat -c %u "$socket")}
case "$peer_uid" in
  ''|*[!0-9]*) fail "Docker peer UID must be an unsigned integer" ;;
esac

allowed_authority=${LOKI_TEST_EGRESS_ALLOWED_AUTHORITY:-registry.npmjs.org:443}
case "$allowed_authority" in
  *:443) allowed_host=${allowed_authority%:443} ;;
  *) fail "allowed authority must use host:443" ;;
esac
test -n "$allowed_host" || fail "allowed authority host is empty"
grep -F "\"$allowed_host\"" "$SOURCE_DIR/packaging/native/egress-policy.json" >/dev/null 2>&1 ||
  fail "allowed authority is not present in packaging/native/egress-policy.json: $allowed_authority"

workspace_owned=0
if test -n "${LOKI_TEST_DOCKER_WORKSPACE:-}"; then
  workspace=$LOKI_TEST_DOCKER_WORKSPACE
  case "$workspace" in
    /*) ;;
    *) fail "shared workspace must be an absolute path" ;;
  esac
  test -d "$workspace" || fail "shared workspace does not exist: $workspace"
  test -w "$workspace" || fail "shared workspace is not writable: $workspace"
else
  workspace=$(mktemp -d "${TMPDIR:-/tmp}/loki-oci-job-acceptance.XXXXXX")
  workspace_owned=1
  chmod 0755 "$workspace"
fi

run_id="$$-$(date +%s)"
registry_image=${LOKI_OCI_ACCEPTANCE_REGISTRY_IMAGE:-registry:3.1.1@sha256:fd374bae807c225661adfe2c0c1f9970a0b8fab1761fd7dfb91e0fd9a8748f9b}
registry_container=
registry_image_preexisting=0
image_tag=
image_digest=

if docker_cmd image inspect "$registry_image" >/dev/null 2>&1; then
  registry_image_preexisting=1
fi

cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if test -n "$registry_container"; then
    docker_cmd rm -f "$registry_container" >/dev/null 2>&1 || true
  fi
  if test -n "$image_tag"; then
    docker_cmd image rm "$image_tag" >/dev/null 2>&1 || true
  fi
  if test -n "$image_digest"; then
    docker_cmd image rm "$image_digest" >/dev/null 2>&1 || true
  fi
  if test "$registry_image_preexisting" -eq 0; then
    docker_cmd image rm "$registry_image" >/dev/null 2>&1 || true
  fi
  if test "$workspace_owned" -eq 1; then
    rm -rf "$workspace"
  fi
  exit "$result"
}
trap cleanup EXIT HUP INT TERM

registry_container=$(docker_cmd run --detach \
  --name "loki-oci-job-registry-$run_id" \
  --publish 127.0.0.1::5000 \
  "$registry_image")

registry_endpoint=$(docker_cmd port "$registry_container" 5000/tcp | sed -n '/^127\.0\.0\.1:[0-9][0-9]*$/p' | head -n 1)
printf '%s\n' "$registry_endpoint" | grep -Eq '^127\.0\.0\.1:[0-9]+$' ||
  fail "temporary registry did not publish an IPv4 loopback port"

image_tag="$registry_endpoint/loki-oci-job-fixture:$run_id"
docker_cmd buildx build \
  --load \
  --file "$SOURCE_DIR/internal/platform/sandbox/testdata/oci-image/Dockerfile" \
  --tag "$image_tag" \
  "$SOURCE_DIR"

attempt=1
while ! docker_cmd push "$image_tag"; do
  test "$attempt" -lt 20 || fail "temporary registry did not accept the fixture image"
  attempt=$((attempt + 1))
  sleep 1
done

image_digest=$(docker_cmd image inspect --format '{{index .RepoDigests 0}}' "$image_tag")
printf '%s\n' "$image_digest" |
  grep -Eq '^127\.0\.0\.1:[0-9]+/loki-oci-job-fixture@sha256:[0-9a-f]{64}$' ||
  fail "fixture image did not resolve to an immutable repository digest"

printf 'loki OCI Job acceptance: socket=%s image=%s authority=%s\n' \
  "$socket" "$image_digest" "$allowed_authority"

LOKI_REQUIRE_OCI_JOB_TESTS=1 \
LOKI_TEST_DOCKER_SOCKET="$socket" \
LOKI_TEST_DOCKER_PEER_UID="$peer_uid" \
LOKI_TEST_DOCKER_IMAGE="$image_digest" \
LOKI_TEST_DOCKER_WORKSPACE="$workspace" \
LOKI_TEST_EGRESS_ALLOWED_AUTHORITY="$allowed_authority" \
  go test ./internal/platform/sandbox -run '^TestRealOCIJob' -v -count=1

printf 'loki OCI Job acceptance: passed\n'
