#!/bin/sh
set -eu
umask 077

test "$#" -eq 0 || { echo 'usage: accept-secret-jobs.sh' >&2; exit 2; }
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
cd "$repo"
provider=${LOKI_SECRET_TEST_DEVTOOLS:?set LOKI_SECRET_TEST_DEVTOOLS to the reviewed exact-source devtools executable}
test -x "$provider"
root=$(mktemp -d /tmp/loki-protected.XXXXXXXX)
chmod 0755 "$root"
fixture_owner="loki-tools-$(printf '%s' "$root" | sha256sum | cut -c1-16)"
registry=
tag=
fixture=
test_container=
cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if test -n "$test_container"; then docker rm -f "$test_container" >/dev/null 2>&1 || true; fi
  if test -n "$fixture"; then
    docker ps -aq --filter "label=io.loki.full.owner=$fixture_owner" | while IFS= read -r id; do
      test -z "$id" || docker rm -f "$id" >/dev/null 2>&1 || true
    done
    docker network ls -q --filter "label=io.loki.full.owner=$fixture_owner" | while IFS= read -r id; do
      test -z "$id" || docker network rm "$id" >/dev/null 2>&1 || true
    done
  fi
  if test -n "$registry"; then docker rm -f "$registry" >/dev/null 2>&1 || true; fi
  if test -n "$tag"; then docker image rm -f "$tag" >/dev/null 2>&1 || true; fi
  # Child fixtures create UID1000/root-owned files; use the same bounded mount
  # to remove only this run's private data before removing the empty directory.
  docker run --rm --network none --mount "type=bind,src=$root,dst=/fixture" \
    alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 \
    sh -c 'find /fixture -mindepth 1 -maxdepth 1 -exec rm -rf {} +' >/dev/null 2>&1 || true
  rmdir "$root" 2>/dev/null || true
  exit "$result"
}
trap cleanup EXIT HUP INT TERM

CGO_ENABLED=0 go test -c -o "$root/launcher.test" ./internal/app/launcher
CGO_ENABLED=0 go build -o "$root/secret-broker-bin" ./cmd/secret-broker
CGO_ENABLED=0 go build -o "$root/network-check" ./internal/platform/sandbox/testdata/oci-fixture-server
chmod 0755 "$root/launcher.test" "$root/secret-broker-bin" "$root/network-check"
registry=$(docker run --detach --publish 127.0.0.1::5000 \
  registry:3.1.1@sha256:fd374bae807c225661adfe2c0c1f9970a0b8fab1761fd7dfb91e0fd9a8748f9b)
endpoint=$(docker port "$registry" 5000/tcp | sed -n '/^127\.0\.0\.1:[0-9][0-9]*$/p' | head -n 1)
test -n "$endpoint"
tag="$endpoint/loki-secret-fixture:$(basename "$root")"
docker buildx build --load --file internal/platform/sandbox/testdata/oci-image/Dockerfile --tag "$tag" .
docker push "$tag"
fixture=$(docker image inspect --format '{{index .RepoDigests 0}}' "$tag")
test_container="loki-secret-test-$(basename "$root")"
printf 'protected OCI acceptance: image=%s provider=%s\n' "$fixture" "$provider"
sha256sum "$provider"
docker run --rm --init --name "$test_container" --read-only --network none \
  --cap-drop ALL --cap-add SETUID --cap-add SETGID --cap-add CHOWN --cap-add KILL --cap-add DAC_OVERRIDE \
  --security-opt no-new-privileges --memory 2g --pids-limit 128 \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=256m \
  --mount "type=bind,src=$root,dst=$root" \
  --mount "type=bind,src=$provider,dst=/fixture/devtools,readonly" \
  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
  -e LOKI_REQUIRE_PROTECTED_OCI=1 \
  -e "LOKI_PROTECTED_FIXTURE_ROOT=$root" \
  -e "LOKI_PROTECTED_OWNER=$fixture_owner" \
  -e "LOKI_TEST_DOCKER_IMAGE=$fixture" \
  -e LOKI_SECRET_TEST_DEVTOOLS=/fixture/devtools \
  -e "LOKI_TEST_SECRET_BROKER=$root/secret-broker-bin" \
  -e "LOKI_PROTECTED_NETWORK_CHECK=$root/network-check" \
  --entrypoint "$root/launcher.test" \
  alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 \
  -test.run '^TestRealProtectedOCIJob$' -test.v -test.timeout 60s
