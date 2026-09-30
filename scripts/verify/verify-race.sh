#!/bin/sh
set -eu

if test "$#" -ne 0; then
  echo "usage: verify-race.sh" >&2
  exit 2
fi

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
cd "$repo"

if test -n "${LOKI_TEST_RG:-}"; then
  test -x "$LOKI_TEST_RG" || {
    echo "loki validation: profile=race prerequisite=LOKI_TEST_RG is not executable: $LOKI_TEST_RG" >&2
    exit 1
  }
else
  LOKI_TEST_RG=$(command -v rg || true)
  test -n "$LOKI_TEST_RG" || {
    echo "loki validation: profile=race prerequisite=ripgrep is required; set LOKI_TEST_RG to a pinned executable" >&2
    exit 1
  }
  export LOKI_TEST_RG
fi

printf 'loki validation: profile=race scope=go-memory-races integration=excluded\n'
go test -race ./... -count=1
printf 'loki validation: profile=race result=passed note=process/container integration is not covered\n'
