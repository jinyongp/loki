#!/bin/sh
set -eu

if test "$#" -ne 0; then
  echo "usage: verify-source.sh" >&2
  exit 2
fi

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
cd "$repo"

if test -n "${LOKI_TEST_RG:-}"; then
  test -x "$LOKI_TEST_RG" || {
    echo "loki validation: profile=source prerequisite=LOKI_TEST_RG is not executable: $LOKI_TEST_RG" >&2
    exit 1
  }
else
  LOKI_TEST_RG=$(command -v rg || true)
  test -n "$LOKI_TEST_RG" || {
    echo "loki validation: profile=source prerequisite=ripgrep is required; set LOKI_TEST_RG to a pinned executable" >&2
    exit 1
  }
  export LOKI_TEST_RG
fi

printf 'loki validation: profile=source scope=deterministic-source integration=excluded\n'

failures=0
run_check() {
  label=$1
  shift
  printf 'loki validation: profile=source check=%s state=running\n' "$label"
  if "$@"; then
    printf 'loki validation: profile=source check=%s result=passed\n' "$label"
    return 0
  fi
  failures=$((failures + 1))
  printf 'loki validation: profile=source check=%s result=failed\n' "$label" >&2
  return 0
}

check_tidy() {
  output=$(go mod tidy -diff) || {
    printf '%s\n' "$output" >&2
    return 1
  }
  test -z "$output" || {
    printf '%s\n' "$output" >&2
    return 1
  }
}

run_check tests go test ./... -count=1
run_check vet go vet ./...
run_check build go build ./...
run_check architecture go run ./tools/archcheck
run_check module-tidiness check_tidy
run_check diff-hygiene git diff --check

if test "$failures" -ne 0; then
  printf 'loki validation: profile=source result=failed failures=%s note=all independent source checks were attempted\n' "$failures" >&2
  exit 1
fi
printf 'loki validation: profile=source result=passed note=fixture-gated integration is not covered\n'
