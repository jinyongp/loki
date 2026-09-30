#!/bin/sh
set -eu

if test "$#" -ne 0; then
  echo "usage: verify-race.sh" >&2
  exit 2
fi

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
cd "$repo"

printf 'loki validation: profile=race scope=go-memory-races integration=excluded\n'
go test -race ./... -count=1
printf 'loki validation: profile=race result=passed note=process/container integration is not covered\n'
