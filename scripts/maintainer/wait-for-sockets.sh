#!/bin/sh
set -eu

ROOT=$LOKI_RUN_ROOT
ATTEMPTS=$LOKI_SOCKET_ATTEMPTS
INTERVAL=$LOKI_SOCKET_INTERVAL

test -n "$ROOT"
case "$ATTEMPTS" in
  *[!0-9]*|'') exit 2 ;;
esac
test "$ATTEMPTS" -gt 0

attempt=0
while test "$attempt" -lt "$ATTEMPTS"; do
  if test -S "$ROOT/runtime/control.sock" &&
     test -S "$ROOT/port-guard/control.sock" &&
     test -S "$ROOT/signing/agent.sock" &&
     test -S "$ROOT/executor/control.sock"; then
    exit 0
  fi
  attempt=$((attempt + 1))
  sleep "$INTERVAL"
done
exit 1
