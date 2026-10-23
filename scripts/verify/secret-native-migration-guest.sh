#!/bin/sh
# Owned no-network VM only; real native CLI, broker, runtime, launcher and MCP.
set -eu
umask 077
exec >/dev/console 2>&1
state=/var/lib/loki-reboot-fixture
operator=/var/lib/loki-migration-operator
phase=native-migration-boot
diagnose() {
  result=$?
  trap - EXIT
  if test "$result" -ne 0; then
    echo "LOKI_SECRET_REBOOT_FAIL phase=$phase exit=$result"
    systemctl status loki-go-mcp.service loki-go-runtime.service loki-go-launcher.service loki-go-executor.service loki-go-secrets.service loki-go-secrets-prepare.service --no-pager || true
    # Services have not run project commands or printed application values.
    journalctl -b -u loki-go-mcp.service -u loki-go-secrets.service -u loki-go-secrets-prepare.service --no-pager -n 30 || true
    journalctl -b -u 'loki-go-secret-migration-*.service' --no-pager -n 20 || true
    systemctl poweroff --no-block || true
  fi
}
trap diagnose EXIT

if test "$1" = first; then
  phase=native-migration-install
  echo "LOKI_SECRET_PHASE=$phase"
  chgrp workspace /etc/loki-go
  chmod 0750 /etc/loki-go
  install -m 0644 /mnt/loki-payload/mcp.service /etc/systemd/system/loki-go-mcp.service
  install -m 0644 /mnt/loki-payload/executor.service /etc/systemd/system/loki-go-executor.service
  install -m 0644 /mnt/loki-payload/host.target /etc/systemd/system/loki-go.target
  # Select the secret journey. Unrelated browser/signing/port modules are not
  # configured in this VM. Keep the actual MCP sandbox and numeric agent UID.
  # systemd dependency directives cannot be reset through a drop-in. Derive
  # this selected fixture's unit before loading it; retain all sandbox lines.
  python3 - <<'PY'
from pathlib import Path
p=Path('/etc/systemd/system/loki-go-mcp.service')
lines=p.read_text().splitlines()
lines=[line for line in lines if not line.startswith(('Requires=','Wants=','After=','ExecStartPre='))]
lines=['User=loki-agent' if line=='User=runner' else line for line in lines]
lines+=['[Unit]','Requires=loki-go-runtime.service loki-go-executor.service loki-go-secrets.service','After=loki-go-runtime.service loki-go-executor.service loki-go-secrets.service']
p.write_text('\n'.join(lines)+'\n')
PY
  install -d -o 21000 -g 21001 -m 0755 /var/lib/loki-go/runner/agents
  # Reproduce the installer's immutable selected-release topology and receipt.
  release=/opt/loki-go/releases/native-migration-fixture
  install -d -m 0755 "$release/opt/loki"
  cp -a /opt/loki/. "$release/opt/loki/"
  (cd "$release"; sha256sum opt/loki/bin/loki > SHA256SUMS)
  ln -s releases/native-migration-fixture /opt/loki-go/current
  mv /opt/loki /opt/loki-fixture-seed
  ln -s /opt/loki-go/current/opt/loki /opt/loki
  /opt/loki/bin/native-migration-peer configure
  install -d -m 0755 /run/loki-go /var/lib/loki-go/secrets-root
  /opt/loki/bin/loki host prepare-secrets --uid 21006 --gid 21006 --agent-uid 21000 --metadata-gid 21001
  cp /etc/loki-go/secrets-deployment.json "$operator/original-identity.json"
  systemctl daemon-reload
  systemctl start loki-go.target
  recipient=$(cat "$operator/recipient")
  if test "$(cat /mnt/loki-payload/scenario)" = rollback; then
  phase=native-migration-rollback
  echo "LOKI_SECRET_PHASE=$phase"
  /opt/loki/bin/loki host secret-migration native prepare --mapping "$operator/mapping.json" --recipient "$recipient" --backup "$operator/source-before-rollback.age" > "$operator/rollback-plan.json"
  /opt/loki/bin/loki host secret-migration native apply --plan "$operator/rollback-plan.json" --approve > "$operator/pending.json"
  python3 - "$operator/pending.json" <<'PY'
import json,sys
r=json.load(open(sys.argv[1]));assert r['phase']=='migrate' and r['protection']=='uninitialized' and not r['released']
PY
  /opt/loki/bin/loki host secret-migration native rollback --approve > "$operator/rolled-back.json"
  cmp /etc/loki-go/secrets-deployment.json "$operator/original-identity.json"
  python3 - "$operator/rolled-back.json" <<'PY'
import json,sys
r=json.load(open(sys.argv[1]));assert r['state']=='rolled_back' and r['released']
PY
  # A completed plan is its original saved result, never another operation.
  /opt/loki/bin/loki host secret-migration native apply --plan "$operator/rollback-plan.json" --approve > "$operator/rollback-replay.json"
  cmp "$operator/rollback-replay.json" "$operator/rolled-back.json"
  echo LOKI_SECRET_NATIVE_ROLLBACK_PASS
  systemctl poweroff --no-block
  exit 0
  fi
  phase=native-migration-pending-reboot
  echo "LOKI_SECRET_PHASE=$phase"
  /opt/loki/bin/loki host secret-migration native prepare --mapping "$operator/mapping.json" --recipient "$recipient" --backup "$operator/source-before-cutover.age" > "$operator/forward-plan.json"
  /opt/loki/bin/loki host secret-migration native apply --plan "$operator/forward-plan.json" --approve > "$operator/pending.json"
  /opt/loki/bin/native-migration-peer initialize-candidate
  /opt/loki/bin/loki host secret-migration native status > "$operator/before-reboot.json"
  cat /proc/sys/kernel/random/boot_id > "$state/boot-id"
  install -d -m 0755 /etc/systemd/system/multi-user.target.wants
  ln -s /etc/systemd/system/loki-go.target /etc/systemd/system/multi-user.target.wants/loki-go.target
  ln -s /etc/systemd/system/loki-reboot-docker.service /etc/systemd/system/multi-user.target.wants/loki-reboot-docker.service
  sync
  echo LOKI_SECRET_REBOOT_FIRST_BOOT_PASS
  systemctl reboot --no-block
else
  phase=native-migration-reboot-fence
  echo "LOKI_SECRET_PHASE=$phase"
  test "$(cat /proc/sys/kernel/random/boot_id)" != "$(cat "$state/boot-id")"
  systemctl start loki-reboot-docker.service
  attempt=0
  until docker info >/dev/null 2>&1; do
    attempt=$((attempt + 1)); test "$attempt" -lt 100; sleep 0.2
  done
  # Direct starts, as well as the suite target, must obey the original fence.
  systemctl start loki-go-secrets.service || true
  test "$(systemctl show loki-go-secrets.service --property ConditionResult --value)" = no
  test "$(systemctl show loki-go-secrets.service --property MainPID --value)" = 0
  test ! -S /run/loki-go/secrets/metadata/control.sock
  /opt/loki/bin/loki host secret-migration native recover > "$operator/recovered-locked.json"
  python3 - "$operator/before-reboot.json" "$operator/recovered-locked.json" <<'PY'
import json,sys
a,b=[json.load(open(p)) for p in sys.argv[1:]]
assert a['operation_id']==b['operation_id'] and b['phase']=='migrate' and b['protection'] in ['locked','locked_manual'] and not b['released']
PY
  phase=native-migration-unlock-cutover
  echo "LOKI_SECRET_PHASE=$phase"
  /opt/loki/bin/loki host secret-migration native recover --protection unlock --stdin < "$operator/passphrase" > "$operator/completed.json"
  python3 - "$operator/completed.json" <<'PY'
import json,sys
r=json.load(open(sys.argv[1]));assert r['state']=='succeeded' and r['released']
PY
  # Normal service restart is cold even though migration was manually unlocked.
  /opt/loki/bin/loki secret protection status | python3 -c 'import json,sys;assert json.load(sys.stdin)["state"] in ["locked","locked_manual"]'
  /opt/loki/bin/native-migration-peer verify
  /opt/loki/bin/loki host secret-migration native apply --plan "$operator/forward-plan.json" --approve > "$operator/forward-replay.json"
  cmp "$operator/forward-replay.json" "$operator/completed.json"
  test ! -e /etc/systemd/system/loki-go-mcp.service.d/70-secret-migration.conf
  test ! -e /etc/systemd/system/loki-go-secrets-prepare.service.d/71-secret-migration-prepare.conf
  test -z "$(docker ps -aq --filter label=io.loki.job.id)"
  echo LOKI_SECRET_REBOOT_PASS
  systemctl poweroff --no-block
fi
