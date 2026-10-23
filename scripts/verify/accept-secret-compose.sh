#!/bin/sh
set -eu
umask 077
test "$#" -ge 1 && test "$#" -le 2 || { echo 'usage: accept-secret-compose.sh LOCAL_NATIVE_IMAGE [normal|restore]' >&2; exit 2; }
image=$1
suite=${2:-normal}
case "$suite" in normal|restore) ;; *) exit 2 ;; esac
registry=
tag=
test_binary=
source_builder=
host_cli=
cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if test -n "$registry"; then docker rm -f "$registry" >/dev/null 2>&1 || true; fi
  if test -n "$tag"; then docker image rm "$tag" >/dev/null 2>&1 || true; fi
  if test -n "$test_binary"; then rm -f "$test_binary"; fi
  if test -n "$source_builder"; then rm -f "$source_builder"; fi
  if test -n "$host_cli"; then rm -f "$host_cli"; fi
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
LOKI_TEST_NATIVE_SECRET_IMAGE=$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$tag" | while IFS= read -r reference; do
  case "$reference" in "$endpoint/loki-native-secret@sha256:"*) printf '%s\n' "$reference" ;; esac
done)
test -n "$LOKI_TEST_NATIVE_SECRET_IMAGE"
export LOKI_TEST_NATIVE_SECRET_IMAGE
export LOKI_REQUIRE_NATIVE_SECRET_COMPOSE=1
source_builder=$(mktemp /tmp/loki-secret-source-builder.XXXXXXXX)
CGO_ENABLED=0 go build -o "$source_builder" ./internal/host/lifecycle/testdata/native-migration
chmod 0555 "$source_builder"
export LOKI_TEST_MIGRATION_SOURCE_BUILDER="$source_builder"
host_cli=$(mktemp /tmp/loki-secret-host-cli.XXXXXXXX)
CGO_ENABLED=0 go build -o "$host_cli" ./cmd/loki
chmod 0555 "$host_cli"
export LOKI_TEST_HOST_MIGRATION_CLI="$host_cli"
if test "$suite" = restore; then
  go test -count=1 -v -timeout=7m ./internal/host/lifecycle/compose -run '^TestRealComposeApplicationRestore$'
  exit 0
fi
go test -count=1 -v -timeout=7m ./internal/host/lifecycle/compose -run '^TestRealCompose(ApplicationService|MigrationPreparationWithoutRunningServices|MigrationPublicLifecycle)$'
test_binary=$(mktemp /tmp/loki-managed-admission.XXXXXXXX)
CGO_ENABLED=0 go test -c -o "$test_binary" ./internal/management
chmod 0555 "$test_binary"
(
  cd internal/management
  "$test_binary" -test.run '^TestRealManagedFull(Admission|MigrationCandidate)$' -test.v -test.timeout=5m
)
