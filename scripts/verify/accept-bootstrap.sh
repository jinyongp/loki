#!/bin/sh
set -eu

if test "$#" -ne 0; then
  echo "usage: accept-bootstrap.sh" >&2
  exit 2
fi

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
cd "$repo"

go test ./internal/host/bootstrap   -run '^TestSourceFreeBootstrapAcceptanceUbuntuAndWSL$'   -count=1

go test ./internal/host/releases   -run '^TestAuthenticatedMetadataAcceptanceFreshnessRollbackAndRotation$'   -count=1

printf 'loki release-bound bootstrap acceptance: passed\n'
