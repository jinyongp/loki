#!/bin/sh
# Disposable NoCloud guest only. The payload contains no unlock credential.
set -eu
umask 077
exec >/dev/console 2>&1
diagnose() {
  result=$?
  trap - EXIT
  if test "$result" -ne 0; then
    echo LOKI_SECRET_REBOOT_FAIL
    systemctl status loki-go-secrets.service loki-go-secrets-prepare.service loki-go-launcher.service loki-go-runtime.service loki-reboot-docker.service --no-pager || true
    journalctl -b -u loki-go-secrets.service -u loki-go-secrets-prepare.service -u loki-go-launcher.service -u loki-go-runtime.service -u loki-reboot-docker.service --no-pager -n 80 || true
    systemctl poweroff --no-block || true
  fi
}
trap diagnose EXIT
state=/var/lib/loki-reboot-fixture
install -d -m 0700 "$state"
status() {
  /opt/loki/bin/loki secret protection status | python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])'
}
instance() {
  python3 -c 'import json; print(json.load(open("/run/loki-go/secrets/private/owner.json"))["instance"])'
}
start_docker() {
  systemctl start loki-reboot-docker.service
  attempt=0
  until docker info >/dev/null 2>&1; do
    attempt=$((attempt + 1))
    test "$attempt" -lt 100
    sleep 0.2
  done
}
coordination() {
  systemctl start loki-go-runtime.service
  runuser -u loki-agent -- /opt/loki/bin/native-reboot-peer coordination
}
if test ! -f "$state/boot-id"; then
  install -d -m 0755 /mnt/loki-payload
  mount -o ro /dev/disk/by-label/LOKIPAYLOAD /mnt/loki-payload
  install -d -m 0755 /opt/loki/bin /usr/share/doc/loki /etc/systemd/system
  install -d -m 0700 /etc/loki-go
  install -m 0555 /mnt/loki-payload/loki /mnt/loki-payload/loki-launcher /mnt/loki-payload/loki-secret-broker /mnt/loki-payload/devtools /mnt/loki-payload/native-reboot-peer /opt/loki/bin/
  install -m 0644 /mnt/loki-payload/secrets.service /etc/systemd/system/loki-go-secrets.service
  install -m 0644 /mnt/loki-payload/secrets-prepare.service /etc/systemd/system/loki-go-secrets-prepare.service
  install -m 0644 /mnt/loki-payload/launcher.service /etc/systemd/system/loki-go-launcher.service
  install -m 0644 /mnt/loki-payload/runtime.service /etc/systemd/system/loki-go-runtime.service
  install -m 0644 /mnt/loki-payload/execution-contract.json /usr/share/doc/loki/execution-contract.json
  install -m 0640 /mnt/loki-payload/config.toml /etc/loki-go/config.toml
  install -m 0644 /mnt/loki-payload/gitconfig /etc/loki-go/gitconfig
  printf '{"resources":[]}\n' > /etc/loki-go/application-resources.json
  groupadd --gid 21001 workspace
  groupadd --gid 21006 loki-secrets
  useradd --uid 21006 --gid 21006 --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin loki-secrets
  useradd --uid 21000 --gid 21001 --home-dir /home/runner --create-home --shell /usr/sbin/nologin loki-agent
  useradd --uid 21004 --gid 21001 --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin loki-executor
  chgrp workspace /etc/loki-go/config.toml
  # Only this disposable guest receives the runner's native Docker engine.
  # Existing Ubuntu24 libraries remain; missing SONAME dependencies are copied.
  find /mnt/loki-payload/docker-runtime -type f | while IFS= read -r input; do
    target=${input#/mnt/loki-payload/docker-runtime}
    if test ! -e "$target"; then install -D -m 0555 "$input" "$target"; fi
  done
  cat > /etc/systemd/system/loki-reboot-docker.service <<'UNIT'
[Unit]
Description=Disposable native reboot fixture Docker engine
After=local-fs.target
Before=loki-go-launcher.service
[Service]
Type=notify
NotifyAccess=all
ExecStart=/usr/bin/dockerd --host=unix:///run/docker.sock --data-root=/var/lib/loki-reboot-docker --exec-root=/run/loki-reboot-docker --init-path=/usr/bin/docker-init --storage-driver=vfs --bridge=none --iptables=false --ip6tables=false --ip-forward=false --userland-proxy=false
TimeoutStartSec=90
TimeoutStopSec=15
UNIT
  systemctl daemon-reload
  start_docker
  docker load --input /mnt/loki-payload/images.tar
  docker run --detach --network host --name loki-reboot-registry "$(cat /mnt/loki-payload/registry-tag)"
  job_tag=127.0.0.1:5000/loki-reboot:fixture
  docker tag "$(cat /mnt/loki-payload/workload-tag)" "$job_tag"
  attempt=0
  until docker push "$job_tag"; do
    attempt=$((attempt + 1))
    test "$attempt" -lt 20
    sleep 0.2
  done
  job_image=$(docker image inspect --format '{{index .RepoDigests 0}}' "$job_tag")
  # Prepare all native contract directories with their real service ownership.
  install -d -m 0755 /var/lib/loki-go /var/cache/loki-go /var/tmp/loki-go /srv/workspace
  python3 - <<'PY'
import json,os
contract=json.load(open('/usr/share/doc/loki/execution-contract.json'))
for name,entry in contract['directories'].items():
    path=entry['path']
    os.makedirs(path, exist_ok=True)
    os.chown(path,21000,21001)
    os.chmod(path,int(entry['mode'],8))
os.makedirs('/srv/workspace/loki/.git/objects',exist_ok=True)
os.makedirs('/srv/workspace/loki/.git/refs',exist_ok=True)
open('/srv/workspace/loki/.git/HEAD','w').write('ref: refs/heads/main\n')
open('/srv/workspace/loki/.git/config','w').write('[core]\nrepositoryformatversion = 0\n')
open('/srv/workspace/loki/devtools.toml','w').write("profile = 'native-fixture'\n")
for path in ['/srv/workspace/loki/.git','/srv/workspace/loki/.git/objects','/srv/workspace/loki/.git/refs']:
    os.chown(path,21000,21001); os.chmod(path,0o755)
for path in ['/srv/workspace/loki/.git/HEAD','/srv/workspace/loki/.git/config','/srv/workspace/loki/devtools.toml']:
    os.chmod(path,0o644)
os.chown('/srv/workspace/loki',21000,21001)
os.chmod('/srv/workspace/loki',0o2770)
for path in ['/var/lib/loki-go/toolchains/generations','/var/lib/loki-go/toolchains/refs','/var/lib/loki-go/signing','/var/lib/loki-go/runtime','/var/tmp/loki-go/github']:
    os.makedirs(path, mode=0o700,exist_ok=True)
for path in ['/etc/loki-go/token']:
    open(path,'w').close()
    os.chmod(path,0o600)
PY
  sh /mnt/loki-payload/render-layouts /mnt/loki-payload /etc/loki-go 21000 21001 21001 21003 21004 "$job_image"
  printf 'SECRETS_UID=21006\nSECRETS_GID=21006\n' >> /etc/loki-go/identity.env
  # Keep the actual installed runtime unit, selecting only coordination so its
  # availability is independently observable with the broker locked/stopped.
  python3 - <<'PY'
import json,os
p='/etc/loki-go/runtime.json'
d=json.load(open(p)); d['Tools']=['coordination']
with open(p,'w') as f: json.dump(d,f)
os.chown(p,0,21001); os.chmod(p,0o640)
PY
  /opt/loki/bin/native-reboot-peer configure
  install -d -m 0755 /run/loki-go /var/lib/loki-go/secrets-root
  /opt/loki/bin/loki host prepare-secrets --uid 21006 --gid 21006 --agent-uid 21000 --metadata-gid 21001
  systemctl daemon-reload
  systemctl start loki-go-secrets.service
  test "$(status)" = uninitialized
  # No credential crosses the host/payload boundary or survives /run.
  head -c 48 /dev/urandom | base64 > /run/loki-fixture.pass
  /opt/loki/bin/loki secret protection init --file /run/loki-fixture.pass
  test "$(status)" = unlocked
  pid=$(systemctl show loki-go-secrets.service --property MainPID --value)
  test "$pid" -gt 1
  test "$(awk '/^Uid:/{print $2}' "/proc/$pid/status")" = 21006
  test "$(awk '/^CapEff:/{print $2}' "/proc/$pid/status")" = 0000000000000000
  test ! -e "/proc/$pid/root/workspace"
  test ! -e "/proc/$pid/root/run/docker.sock"
  test ! -e "/proc/$pid/root/etc/loki-go/secrets-deployment.json"
  test ! -e "/proc/$pid/root/var/lib/loki-go/secrets/generations"
  test ! -e "/proc/$pid/root/opt/loki/bin/loki"
  instance > "$state/instance-before-restart"
  systemctl restart loki-go-secrets.service
  case "$(status)" in locked|locked_manual) ;; *) exit 1;; esac
  test "$(instance)" != "$(cat "$state/instance-before-restart")"
  # Broker unavailability must not take down the independent native runtime.
  systemctl stop loki-go-secrets.service
  coordination
  systemctl start loki-go-secrets.service
  case "$(status)" in locked|locked_manual) ;; *) exit 1;; esac
  /opt/loki/bin/loki secret protection unlock --file /run/loki-fixture.pass
  test "$(status)" = unlocked
  /opt/loki/bin/native-reboot-peer seed
  systemctl start loki-go-launcher.service
  runuser -u loki-executor -- /opt/loki/bin/native-reboot-peer start
  /opt/loki/bin/native-reboot-peer journal
  cat /proc/sys/kernel/random/boot_id > "$state/boot-id"
  instance > "$state/instance-before-reboot"
  # Start the exact installed service on the next boot without involving the
  # unrelated suite's browser, Docker, provider, or workspace dependencies.
  install -d -m 0755 /etc/systemd/system/multi-user.target.wants
  ln -s /etc/systemd/system/loki-go-secrets.service /etc/systemd/system/multi-user.target.wants/loki-go-secrets.service
  ln -s /etc/systemd/system/loki-reboot-docker.service /etc/systemd/system/multi-user.target.wants/loki-reboot-docker.service
  sync
  echo LOKI_SECRET_REBOOT_FIRST_BOOT_PASS
  systemctl reboot --no-block
else
  test "$(cat /proc/sys/kernel/random/boot_id)" != "$(cat "$state/boot-id")"
  test ! -e /run/loki-fixture.pass
  start_docker
  systemctl start loki-go-secrets.service
  case "$(status)" in locked|locked_manual) ;; *) exit 1;; esac
  test "$(instance)" != "$(cat "$state/instance-before-reboot")"
  systemctl start loki-go-launcher.service
  runuser -u loki-executor -- /opt/loki/bin/native-reboot-peer check
  /opt/loki/bin/native-reboot-peer journal
  test -z "$(docker ps -aq --filter label=io.loki.job.id)"
  coordination
  echo LOKI_SECRET_REBOOT_PASS
  systemctl poweroff --no-block
fi
