#!/bin/sh
set -eu

if test "$#" -ne 6; then
  echo "usage: render-loki-go-layouts.sh TEMPLATE_DIR OUTPUT_DIR RUNNER_UID RUNNER_GID WORKSPACE_GID BROWSER_UID" >&2
  exit 2
fi

TEMPLATES=$1
OUTPUT=$2
RUNNER_UID=$3
RUNNER_GID=$4
WORKSPACE_GID=$5
BROWSER_UID=$6

case "$TEMPLATES:$OUTPUT" in
  /*:/*) ;;
  *) echo "template and output directories must be absolute" >&2; exit 2 ;;
esac
for value in "$RUNNER_UID" "$RUNNER_GID" "$WORKSPACE_GID" "$BROWSER_UID"; do
  case "$value" in
    *[!0-9]*|'') echo "service identities must be numeric" >&2; exit 2 ;;
  esac
done
test -f "$TEMPLATES/runtime.json.in"
test -f "$TEMPLATES/mcp.json.in"
install -d -m 0750 "$OUTPUT"

render() {
  sed -e "s/@RUNNER_UID@/$RUNNER_UID/g" -e "s/@RUNNER_GID@/$RUNNER_GID/g" -e "s/@WORKSPACE_GID@/$WORKSPACE_GID/g" -e "s/@BROWSER_UID@/$BROWSER_UID/g" "$1" > "$2"
  chmod 0640 "$2"
}

render "$TEMPLATES/runtime.json.in" "$OUTPUT/runtime.json"
render "$TEMPLATES/mcp.json.in" "$OUTPUT/mcp.json"
printf 'RUNNER_UID=%s\nRUNNER_GID=%s\nWORKSPACE_GID=%s\nBROWSER_UID=%s\n' "$RUNNER_UID" "$RUNNER_GID" "$WORKSPACE_GID" "$BROWSER_UID" > "$OUTPUT/identity.env"
chmod 0640 "$OUTPUT/identity.env"
