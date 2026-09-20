#!/bin/sh
set -eu

if test "$#" -ne 8; then
  echo "usage: render-loki-go-layouts.sh TEMPLATE_DIR OUTPUT_DIR RUNNER_UID RUNNER_GID WORKSPACE_GID BROWSER_UID EXECUTOR_UID JOB_IMAGE" >&2
  exit 2
fi

TEMPLATES=$1
OUTPUT=$2
RUNNER_UID=$3
RUNNER_GID=$4
WORKSPACE_GID=$5
BROWSER_UID=$6
EXECUTOR_UID=$7
JOB_IMAGE=$8

case "$TEMPLATES:$OUTPUT" in
  /*:/*) ;;
  *) echo "template and output directories must be absolute" >&2; exit 2 ;;
esac
for value in "$RUNNER_UID" "$RUNNER_GID" "$WORKSPACE_GID" "$BROWSER_UID" "$EXECUTOR_UID"; do
  case "$value" in
    *[!0-9]*|'') echo "service identities must be numeric" >&2; exit 2 ;;
  esac
done
test "$RUNNER_UID" -gt 0
test "$BROWSER_UID" -gt 0
test "$EXECUTOR_UID" -gt 0
test "$EXECUTOR_UID" -ne "$RUNNER_UID"
test "$EXECUTOR_UID" -ne "$BROWSER_UID"
printf '%s\n' "$JOB_IMAGE" | grep -Eq '^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]{1,5})?(/[a-z0-9]+([._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}$'

for template in runtime.json.in mcp.json.in launcher.json.in executor.json.in; do
  test -f "$TEMPLATES/$template"
done
install -d -m 0750 "$OUTPUT"
if test "$(id -u)" -eq 0; then chgrp "$WORKSPACE_GID" "$OUTPUT"; fi

render() {
  sed     -e "s/@RUNNER_UID@/$RUNNER_UID/g"     -e "s/@RUNNER_GID@/$RUNNER_GID/g"     -e "s/@WORKSPACE_GID@/$WORKSPACE_GID/g"     -e "s/@BROWSER_UID@/$BROWSER_UID/g"     -e "s/@EXECUTOR_UID@/$EXECUTOR_UID/g"     -e "s|@JOB_IMAGE@|$JOB_IMAGE|g"     "$1" > "$2"
  chmod 0640 "$2"
  if test "$(id -u)" -eq 0; then chgrp "$WORKSPACE_GID" "$2"; fi
}

render "$TEMPLATES/runtime.json.in" "$OUTPUT/runtime.json"
render "$TEMPLATES/mcp.json.in" "$OUTPUT/mcp.json"
render "$TEMPLATES/launcher.json.in" "$OUTPUT/launcher.json"
render "$TEMPLATES/executor.json.in" "$OUTPUT/executor.json"
printf 'RUNNER_UID=%s\nRUNNER_GID=%s\nWORKSPACE_GID=%s\nBROWSER_UID=%s\nEXECUTOR_UID=%s\n' \
  "$RUNNER_UID" "$RUNNER_GID" "$WORKSPACE_GID" "$BROWSER_UID" "$EXECUTOR_UID" > "$OUTPUT/identity.env"
chmod 0640 "$OUTPUT/identity.env"
if test "$(id -u)" -eq 0; then chgrp "$WORKSPACE_GID" "$OUTPUT/identity.env"; fi
