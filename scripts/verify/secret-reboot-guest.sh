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
    systemctl status loki-go-secrets.service loki-go-secrets-prepare.service --no-pager || true
    journalctl -b -u loki-go-secrets.service -u loki-go-secrets-prepare.service --no-pager -n 50 || true
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
if test ! -f "$state/boot-id"; then
  install -d -m 0755 /mnt/loki-payload
  mount -o ro /dev/disk/by-label/LOKIPAYLOAD /mnt/loki-payload
  install -d -m 0755 /opt/loki/bin /usr/share/doc/loki /etc/systemd/system
  install -d -m 0700 /etc/loki-go
  install -m 0555 /mnt/loki-payload/loki /mnt/loki-payload/loki-secret-broker /mnt/loki-payload/devtools /opt/loki/bin/
  install -m 0644 /mnt/loki-payload/secrets.service /etc/systemd/system/loki-go-secrets.service
  install -m 0644 /mnt/loki-payload/secrets-prepare.service /etc/systemd/system/loki-go-secrets-prepare.service
  install -m 0644 /mnt/loki-payload/execution-contract.json /usr/share/doc/loki/execution-contract.json
  install -m 0600 /mnt/loki-payload/config.toml /etc/loki-go/config.toml
  printf '{"resources":[]}\n' > /etc/loki-go/application-resources.json
  groupadd --gid 21001 workspace
  groupadd --gid 21006 loki-secrets
  useradd --uid 21006 --gid 21006 --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin loki-secrets
  useradd --uid 21000 --gid 21001 --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin loki-agent
  printf 'RUNNER_UID=21000\nWORKSPACE_GID=21001\nSECRETS_UID=21006\nSECRETS_GID=21006\n' > /etc/loki-go/identity.env
  install -d -m 0755 /run/loki-go /var/lib/loki-go/secrets-root
  /opt/loki/bin/loki host prepare-secrets --uid 21006 --gid 21006 --agent-uid 21000 --metadata-gid 21001 --overlay-directory=
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
  /opt/loki/bin/loki secret protection unlock --file /run/loki-fixture.pass
  test "$(status)" = unlocked
  cat /proc/sys/kernel/random/boot_id > "$state/boot-id"
  instance > "$state/instance-before-reboot"
  # Start the exact installed service on the next boot without involving the
  # unrelated suite's browser, Docker, provider, or workspace dependencies.
  install -d -m 0755 /etc/systemd/system/multi-user.target.wants
  ln -s /etc/systemd/system/loki-go-secrets.service /etc/systemd/system/multi-user.target.wants/loki-go-secrets.service
  sync
  echo LOKI_SECRET_REBOOT_FIRST_BOOT_PASS
  systemctl reboot --no-block
else
  test "$(cat /proc/sys/kernel/random/boot_id)" != "$(cat "$state/boot-id")"
  test ! -e /run/loki-fixture.pass
  systemctl start loki-go-secrets.service
  case "$(status)" in locked|locked_manual) ;; *) exit 1;; esac
  test "$(instance)" != "$(cat "$state/instance-before-reboot")"
  echo LOKI_SECRET_REBOOT_PASS
  systemctl poweroff --no-block
fi
