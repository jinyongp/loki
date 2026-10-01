#!/bin/sh
set -eu

oci=0
case "$#:$*" in
  0:) ;;
  1:--oci) oci=1 ;;
  *) echo "usage: verify-preflight.sh [--oci]" >&2; exit 2 ;;
esac

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)

printf 'loki validation: profile=preflight required=source,race oci=%s\n' "$oci"

failures=0
run_check() {
  label=$1
  shift
  printf 'loki validation: profile=preflight check=%s state=running\n' "$label"
  if "$@"; then
    printf 'loki validation: profile=preflight check=%s result=passed\n' "$label"
    return 0
  fi
  failures=$((failures + 1))
  printf 'loki validation: profile=preflight check=%s result=failed\n' "$label" >&2
  return 0
}

run_check source sh "$repo/scripts/verify/verify-source.sh"
run_check race sh "$repo/scripts/verify/verify-race.sh"
if test "$oci" -eq 1; then
  run_check real-oci sh "$repo/scripts/verify/accept-oci-jobs.sh"
fi

if test "$failures" -ne 0; then
  printf 'loki validation: profile=preflight result=failed failures=%s note=all independent preflight profiles were attempted\n' "$failures" >&2
  exit 1
fi
printf 'loki validation: profile=preflight result=passed oci=%s remaining=exact-candidate,windows-wsl,publication\n' "$oci"
