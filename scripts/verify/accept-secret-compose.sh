#!/bin/sh
set -eu
umask 077
test "$#" -eq 1 || { echo 'usage: accept-secret-compose.sh LOCAL_NATIVE_IMAGE' >&2; exit 2; }
image=$1
registry=
tag=
cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if test -n "$registry"; then docker rm -f "$registry" >/dev/null 2>&1 || true; fi
  if test -n "$tag"; then docker image rm "$tag" >/dev/null 2>&1 || true; fi
  exit "$result"
}
trap cleanup EXIT HUP INT TERM
registry=$(docker run --detach --publish 127.0.0.1::5000 \
  registry:3.1.1@sha256:fd374bae807c225661adfe2c0c1f9970a0b8fab1761fd7dfb91e0fd9a8748f9b)
endpoint=$(docker port "$registry" 5000/tcp | sed -n '/^127\.0\.0\.1:[0-9][0-9]*$/p' | head -n 1)
test -n "$endpoint"
tag="$endpoint/loki-native-secret:fixture"
docker tag "$image" "$tag"
docker push "$tag"
LOKI_TEST_NATIVE_SECRET_IMAGE=$(docker image inspect --format '{{index .RepoDigests 0}}' "$tag")
export LOKI_TEST_NATIVE_SECRET_IMAGE
export LOKI_REQUIRE_NATIVE_SECRET_COMPOSE=1
go test -count=1 -v -timeout=3m ./internal/host/lifecycle/compose -run '^TestRealComposeApplicationService$'
