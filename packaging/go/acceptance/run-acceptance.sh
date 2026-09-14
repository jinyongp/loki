#!/bin/sh
set -eu

phase=${1:?phase required}
candidate=${2:?candidate path required}
fixture=${3:?fixture path required}
state=/var/lib/loki-acceptance
source=$state/python-copy
record=$state/source.stat

diagnose() {
  result=$?
  trap - EXIT HUP INT TERM
  if test "$result" -ne 0; then
    systemctl --failed --no-pager || true
    journalctl -b --no-pager -u 'loki-go*' -n 300 || true
  fi
  exit "$result"
}
trap diagnose EXIT HUP INT TERM

source_state() {
  printf '%s %s %s %s\n' \
    "$(stat -c %i "$source/master.key")" "$(sha256sum "$source/master.key" | cut -d' ' -f1)" \
    "$(stat -c %i "$source/store.json")" "$(sha256sum "$source/store.json" | cut -d' ' -f1)"
}

wait_systemd() {
  attempt=0
  while test "$attempt" -lt 100; do
    systemctl is-system-running --quiet 2>/dev/null && return 0
    test "$(systemctl is-system-running 2>/dev/null || true)" = degraded && return 0
    attempt=$((attempt + 1))
    sleep 0.1
  done
  return 1
}

case "$phase" in
  prepare)
    wait_systemd
    install -d -m 0700 "$state" "$source"
    python3 - "$fixture" "$source" <<'PY'
import json
import os
import pathlib
import sys

fixture = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
target = pathlib.Path(sys.argv[2])
(target / "master.key").write_bytes(bytes.fromhex(fixture["key_hex"]))
(target / "store.json").write_text(
    json.dumps(fixture["envelope"], separators=(",", ":")) + "\n",
    encoding="utf-8",
)
os.chmod(target / "master.key", 0o600)
os.chmod(target / "store.json", 0o600)
PY
    source_state > "$record"
    touch /run/docker.sock
    groupadd --gid 21001 workspace
    groupadd --gid 21000 runner
    useradd --uid 21000 --gid runner --groups workspace --home-dir /home/runner --create-home --shell /bin/bash runner
    useradd --uid 21001 --gid workspace --home-dir /var/lib/loki-go/browser --no-create-home --shell /usr/sbin/nologin loki-browser
    runuser -u runner -- env HOME=/home/runner git config --global user.name "Loki Acceptance"
    runuser -u runner -- env HOME=/home/runner git config --global user.email "loki-acceptance@example.test"
    "$candidate/install.sh" install "$candidate" acceptance-v1 21000 21000 21001 21001 "$source"
    LOKI_SKIP_APT=1 "$candidate/install.sh" install "$candidate" acceptance-v2 21000 21000 21001 21001
    echo "acceptance: health"
    /usr/local/sbin/loki-go-lifecycle health
    echo "acceptance: enabled/current"
    systemctl is-enabled --quiet loki-go.target
    test "$(readlink /opt/loki-go/current)" = releases/acceptance-v2
    echo "acceptance: secret status"
    /usr/local/bin/loki secret status | grep -q '"initialized": true'
    echo "acceptance: devtools"
    runuser -u runner -- /usr/local/bin/devtools version | grep -q '"ok":true'
    echo "acceptance: toolchain"
    /opt/loki/bin/loki toolchain doctor --manifest /usr/share/doc/loki/toolchain-manifest.json >/dev/null
    echo "acceptance: sockets/browser"
    test -S /run/loki-go/browser/control.sock
    test -S /run/loki-go/runtime/control.sock
    runuser -u runner -- python3 - <<'PY'
import json
import socket

client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
client.connect("/run/loki-go/browser/control.sock")
client.sendall(b'{"operation":"start","arguments":{}}\n')
response = b""
while not response.endswith(b"\n"):
    chunk = client.recv(65536)
    if not chunk:
        raise RuntimeError("browser RPC closed without a response")
    response += chunk
result = json.loads(response)
if result.get("ok") is not True:
    raise RuntimeError(f"browser did not start: {result}")
PY
    pgrep -u loki-browser -f '/opt/loki/toolchain/bin/chromium' >/dev/null
    echo "acceptance: git signing"
    signing_repo=/srv/workspace/loki/signing-acceptance
    runuser -u runner -- mkdir -p "$signing_repo"
    runuser -u runner -- git -C "$signing_repo" init -q
    runuser -u runner -- sh -c 'printf verified > "$1/verified.txt"' sh "$signing_repo"
    runuser -u runner -- git -C "$signing_repo" add verified.txt
    runuser -u runner -- env GIT_CONFIG_GLOBAL=/etc/loki-go/gitconfig GIT_CONFIG_NOSYSTEM=1 SSH_AUTH_SOCK=/run/loki-go/signing/agent.sock git -C "$signing_repo" commit -qm "test: verify candidate signing"
    runuser -u runner -- env GIT_CONFIG_GLOBAL=/etc/loki-go/gitconfig GIT_CONFIG_NOSYSTEM=1 git -C "$signing_repo" verify-commit HEAD
    echo "acceptance: source invariant"
    source_state | cmp - "$record"
    ;;
  verify)
    wait_systemd
    touch /run/docker.sock
    /usr/local/sbin/loki-go-lifecycle health
    test "$(readlink /opt/loki-go/current)" = releases/acceptance-v2
    /usr/local/sbin/loki-go-lifecycle rollback
    test "$(readlink /opt/loki-go/current)" = releases/acceptance-v1
    /usr/local/bin/loki migrate-vault restore --migration /var/lib/loki-go/runtime --destination "$state/python-restore" >/dev/null
    cmp "$source/master.key" "$state/python-restore/master.key"
    cmp "$source/store.json" "$state/python-restore/store.json"
    source_state | cmp - "$record"
    /usr/local/bin/loki secret status | grep -q '"initialized": true'
    /usr/local/sbin/loki-go-lifecycle health
    ;;
  *) echo "usage: run-loki-go-acceptance prepare|verify CANDIDATE FIXTURE" >&2; exit 2 ;;
esac
