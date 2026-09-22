#!/bin/sh
set -eu

REPO_DIR=${1:-$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)}
REPO_DIR=$(CDPATH= cd -- "$REPO_DIR" && pwd)
SOURCE_DIR="$REPO_DIR/legacy/python"
test -f "$SOURCE_DIR/pyproject.toml"
test -d "$REPO_DIR/bundled_skills"
test -f "$REPO_DIR/config/gitconfig"
VERIFY_DIR=$(mktemp -d /tmp/loki-verify.XXXXXX)
cleanup() {
  status=$?
  if test "$status" -ne 0; then
    journalctl -u loki-runtime.service -u loki-port-guard.service -u loki-browser-proxy.service -u loki-browser.service --no-pager -n 40 || true
  fi
  rm -rf "$VERIFY_DIR"
  return "$status"
}
trap cleanup EXIT INT TERM

python3 -m venv "$VERIFY_DIR/venv"
"$VERIFY_DIR/venv/bin/python" -m pip install --quiet --upgrade pip
"$VERIFY_DIR/venv/bin/python" -m pip install --quiet "$SOURCE_DIR[test]"
"$VERIFY_DIR/venv/bin/python" -m pytest -q "$SOURCE_DIR/tests"

/bin/sh "$SOURCE_DIR/scripts/install-loki-mcp.sh" "$REPO_DIR"
attempt=0
while test ! -S /run/loki/browser/control.sock && test "$attempt" -lt 40; do
  sleep 0.25
  attempt=$((attempt + 1))
done
test -S /run/loki/browser/control.sock
/opt/loki-mcp/venv/bin/python "$SOURCE_DIR/scripts/verify-browser-local-bridge.py"
/opt/loki-mcp/venv/bin/python "$SOURCE_DIR/scripts/verify-image-widget.py"
/opt/loki-mcp/venv/bin/python "$SOURCE_DIR/scripts/verify-live-preview.py"
/opt/loki-mcp/venv/bin/python "$SOURCE_DIR/scripts/verify-agent-skills.py"
/opt/loki-mcp/venv/bin/python "$SOURCE_DIR/scripts/verify-artifact-viewers.py"
/opt/loki-mcp/venv/bin/python "$SOURCE_DIR/scripts/verify-git-signing.py"
/opt/loki-mcp/venv/bin/python "$SOURCE_DIR/scripts/verify-runtime.py"
/opt/loki-mcp/venv/bin/python "$SOURCE_DIR/scripts/verify-progress-guidance.py"
systemctl is-active loki-signing-agent.service loki-runtime.service loki-port-guard.service loki-browser-proxy.service loki-browser.service loki-mcp.service
/opt/loki-mcp/venv/bin/python -c 'import importlib.metadata; print("loki=" + importlib.metadata.version("loki-mcp"))'
