#!/bin/sh
set -eu

if test "$#" -ne 1; then
  echo "usage: accept-candidate.sh CANDIDATE" >&2
  exit 2
fi

SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
CANDIDATE=$(realpath "$1")
test -x "$CANDIDATE/install.sh"
RUN_ID="$$-$(date +%s)"
IMAGE="loki-go-acceptance:$RUN_ID"
containers=

cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  for container in $containers; do
    docker rm -f "$container" >/dev/null 2>&1 || true
  done
  docker image rm "$IMAGE" >/dev/null 2>&1 || true
  exit "$result"
}
trap cleanup EXIT HUP INT TERM

docker buildx version >/dev/null 2>&1 || { echo "Docker Buildx with BuildKit is required" >&2; exit 1; }
docker buildx build --quiet --load --tag "$IMAGE" "$SOURCE_DIR/packaging/native/acceptance"

pass=1
passes=${LOKI_ACCEPTANCE_PASSES:-2}
case "$passes" in ''|*[!0-9]*) echo "LOKI_ACCEPTANCE_PASSES must be a positive integer" >&2; exit 2 ;; esac
test "$passes" -gt 0 || { echo "LOKI_ACCEPTANCE_PASSES must be positive" >&2; exit 2; }
while test "$pass" -le "$passes"; do
  name="loki-go-acceptance-$RUN_ID-$pass"
  container=$(docker run --detach --privileged --cgroupns=host \
    --name "$name" \
    --tmpfs /run --tmpfs /run/lock \
    --volume /sys/fs/cgroup:/sys/fs/cgroup:rw \
    --volume "$CANDIDATE:/candidate:ro" \
    --volume "$SOURCE_DIR/internal/state/testdata/python-v1.json:/fixture/python-v1.json:ro" \
    "$IMAGE")
  containers="$containers $container"
  docker exec "$container" /usr/local/libexec/run-loki-go-acceptance prepare /candidate /fixture/python-v1.json
  docker restart "$container" >/dev/null
  docker exec "$container" /usr/local/libexec/run-loki-go-acceptance verify /candidate /fixture/python-v1.json
  docker rm -f "$container" >/dev/null
  containers=$(printf '%s\n' "$containers" | sed "s/ $container//")
  pass=$((pass + 1))
done

echo "Loki Go candidate acceptance passed $passes time(s)."
