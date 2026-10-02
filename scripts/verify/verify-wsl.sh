#!/bin/sh
set -eu
umask 077

if test "$#" -ne 3; then
  echo "usage: verify-wsl.sh WSL_ARCHIVE HOST_BINARY RELEASE_MANIFEST" >&2
  exit 2
fi

archive=$1
host=$2
manifest=$3
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
requirements=$repo/internal/host/appliance/requirements.tsv

test -f "$archive" || { echo "WSL archive is missing" >&2; exit 1; }
test -x "$host" || { echo "host binary is missing or not executable" >&2; exit 1; }
test -f "$manifest" || { echo "release manifest is missing" >&2; exit 1; }

gzip -t "$archive"
listing=$(mktemp)
metadata=$(mktemp)
root=$(mktemp -d)
cleanup() { rm -f "$listing" "$metadata"; rm -rf "$root"; }
trap cleanup EXIT HUP INT TERM

tar -tzf "$archive" | sed 's#^\./##' >"$listing"
tar --numeric-owner -tvzf "$archive" >"$metadata"
awk '
  /^\// { bad=1 }
  {
    n=split($0, p, "/")
    for (i=1; i<=n; i++) if (p[i] == "..") bad=1
  }
  END { exit bad ? 1 : 0 }
' "$listing" || {
  echo "WSL archive contains an unsafe path" >&2
  exit 1
}

for required in \
  etc/wsl.conf \
  etc/wsl-distribution.conf \
  etc/passwd \
  etc/group \
  etc/shadow \
  var/lib/systemd/linger/ubuntu \
  usr/lib/loki-appliance/requirements.tsv \
  usr/lib/loki-appliance/loki \
  usr/lib/loki-appliance/release-manifest.json \
  usr/lib/loki-appliance/configure-install \
  usr/lib/loki-appliance/provision \
  usr/lib/systemd/system/loki-appliance-provision.service \
  etc/systemd/system/multi-user.target.wants/loki-appliance-provision.service \
  home/ubuntu/workspace
do
  if ! grep -Fxq "$required" "$listing" && ! grep -Fxq "$required/" "$listing"; then
    echo "WSL archive lacks $required" >&2
    exit 1
  fi
done

for forbidden in \
  etc/resolv.conf \
  etc/apt/keyrings/docker.asc \
  etc/apt/sources.list.d/docker.sources \
  var/log/apt/history.log \
  var/log/apt/term.log \
  var/log/dpkg.log \
  var/log/alternatives.log \
  var/lib/loki/lifecycle/mcp-token \
  var/lib/loki-appliance/mcp-port \
  var/lib/loki-appliance/provisioned
do
  if grep -Fxq "$forbidden" "$listing"; then
    echo "WSL archive contains forbidden pre-provisioned state: $forbidden" >&2
    exit 1
  fi
done

if grep -Eq '(^|/)boot/(vmlinuz|initrd|initramfs)' "$listing"; then
  echo "WSL archive must not contain a kernel or initramfs" >&2
  exit 1
fi

for root_owned in \
  etc/wsl.conf \
  etc/wsl-distribution.conf \
  var/lib/systemd/linger/ubuntu \
  usr/lib/loki-appliance/loki \
  usr/lib/loki-appliance/release-manifest.json \
  usr/lib/loki-appliance/configure-install
do
  awk -v path="$root_owned" '$NF == path && $2 == "0/0" { ok=1 } END { exit ok ? 0 : 1 }' "$metadata" || {
    echo "WSL archive path is not root-owned: $root_owned" >&2
    exit 1
  }
done
awk '$NF == "home/ubuntu/workspace/" && $2 == "1000/1000" { ok=1 } END { exit ok ? 0 : 1 }' "$metadata" || {
  echo "WSL workspace is not owned by UID/GID 1000" >&2
  exit 1
}

tar --same-permissions -xzf "$archive" -C "$root"

test "$(stat -c '%a' "$root/usr/lib/loki-appliance")" = 755 || {
  echo "WSL appliance directory must be traversable with mode 755" >&2
  exit 1
}
cmp "$requirements" "$root/usr/lib/loki-appliance/requirements.tsv"
tab=$(printf '\t')
while IFS="$tab" read -r package version path kind unit peer; do
  case "$package" in ''|'#'*) continue ;; esac
  resolved=$root$path
  links=0
  while test -L "$resolved"; do
    links=$((links + 1))
    test "$links" -le 40 || { echo "WSL archive has a symlink loop: $path" >&2; exit 1; }
    target=$(readlink "$resolved")
    case "$target" in
      /*) resolved=$root$target ;;
      *) resolved=$(dirname "$resolved")/$target ;;
    esac
  done
  test -f "$resolved" || { echo "WSL archive lacks $path" >&2; exit 1; }
  if test "$kind" = executable; then
    test -x "$resolved" || { echo "WSL archive file is not executable: $path" >&2; exit 1; }
  fi
done <"$requirements"

cmp "$host" "$root/usr/lib/loki-appliance/loki"
cmp "$manifest" "$root/usr/lib/loki-appliance/release-manifest.json"
test "$(stat -c '%a' "$root/etc/wsl.conf")" = 644
test "$(stat -c '%a' "$root/etc/wsl-distribution.conf")" = 644
test "$(stat -c '%a' "$root/usr/lib/loki-appliance/release-manifest.json")" = 600
test "$(stat -c '%a' "$root/usr/lib/loki-appliance/loki")" = 755
test "$(stat -c '%a' "$root/usr/lib/loki-appliance/configure-install")" = 755
test "$(stat -c '%a' "$root/home/ubuntu/workspace")" = 750
test -f "$root/var/lib/systemd/linger/ubuntu"
test ! -L "$root/var/lib/systemd/linger/ubuntu"
test ! -s "$root/var/lib/systemd/linger/ubuntu"
test "$(stat -c '%a' "$root/var/lib/systemd/linger/ubuntu")" = 644

grep -Fxq 'systemd=true' "$root/etc/wsl.conf"
grep -Fxq 'default=ubuntu' "$root/etc/wsl.conf"
grep -Fxq 'defaultUid=1000' "$root/etc/wsl-distribution.conf"
if grep -Eq '^[[:space:]]*command[[:space:]]*=' "$root/etc/wsl-distribution.conf"; then
  echo "WSL appliance must not invoke an interactive OOBE command" >&2
  exit 1
fi

grep -Eq '^root:[^:]*:0:0:' "$root/etc/passwd"
grep -Eq '^ubuntu:[^:]*:1000:1000:' "$root/etc/passwd"
if grep -E '^docker:' "$root/etc/group" | grep -Eq '(^|,)ubuntu(,|$)'; then
  echo "default WSL user must not belong to the docker group" >&2
  exit 1
fi
if test -e "$root/etc/sudoers.d/loki-operator"; then
  echo "default WSL user must use the explicit WSL root boundary, not passwordless Loki sudo" >&2
  exit 1
fi
if grep -R -E '^[[:space:]]*ubuntu[[:space:]].*NOPASSWD' "$root/etc/sudoers" "$root/etc/sudoers.d" 2>/dev/null; then
  echo "default WSL user must not receive passwordless sudo authority" >&2
  exit 1
fi
if awk -F: '$2 ~ /^\$/ { found=1 } END { exit found ? 0 : 1 }' "$root/etc/shadow"; then
  echo "WSL appliance contains a password hash" >&2
  exit 1
fi
grep -Eq '^ubuntu:[^:]*::' "$root/etc/shadow" || {
  echo "default WSL user shadow last-change field is not normalized" >&2
  exit 1
}

for unit in \
  systemd-resolved.service \
  systemd-networkd.service \
  NetworkManager.service \
  systemd-tmpfiles-setup.service \
  systemd-tmpfiles-clean.service \
  systemd-tmpfiles-clean.timer \
  systemd-tmpfiles-setup-dev-early.service \
  systemd-tmpfiles-setup-dev.service \
  tmp.mount
do
  test -L "$root/etc/systemd/system/$unit" || {
    echo "WSL-conflicting systemd unit is not masked: $unit" >&2
    exit 1
  }
  test "$(readlink "$root/etc/systemd/system/$unit")" = /dev/null || {
    echo "WSL-conflicting systemd unit mask changed: $unit" >&2
    exit 1
  }
done

test -L "$root/etc/systemd/system/multi-user.target.wants/docker.service"
test -L "$root/etc/systemd/system/multi-user.target.wants/loki-appliance-provision.service"
grep -Fq 'host install' "$root/usr/lib/loki-appliance/provision"
grep -Fq -- '--system' "$root/usr/lib/loki-appliance/provision"
grep -Fq -- '--workspace "$workspace"' "$root/usr/lib/loki-appliance/provision"
grep -Fq -- '--mcp-port "$mcp_port"' "$root/usr/lib/loki-appliance/provision"
grep -Fq 'host doctor --system' "$root/usr/lib/loki-appliance/provision"
grep -Fq 'systemctl start --no-block loki-appliance-provision.service' "$root/usr/lib/loki-appliance/configure-install"
grep -Fq 'ConditionPathExists=/var/lib/loki-appliance/mcp-port' "$root/usr/lib/systemd/system/loki-appliance-provision.service"
