#!/bin/sh
set -eu

if test "$#" -ne 0; then
  echo "usage: verify-preflight.sh" >&2
  exit 2
fi

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)

case $(uname -s) in
  Linux) ;;
  *)
    echo "loki validation: profile=preflight requires Linux because real OCI integration is mandatory" >&2
    exit 1
    ;;
esac

printf 'loki validation: profile=preflight required=source,race,real-oci\n'

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
run_check real-oci sh "$repo/scripts/verify/accept-oci-jobs.sh"

if test "$failures" -ne 0; then
  printf 'loki validation: profile=preflight result=failed failures=%s note=all independent preflight profiles were attempted\n' "$failures" >&2
  exit 1
fi
printf 'loki validation: profile=preflight result=passed remaining=exact-candidate,windows-wsl,publication\n'
