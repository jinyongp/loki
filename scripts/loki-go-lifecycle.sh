#!/bin/sh
set -eu

ROOT=${LOKI_INSTALL_ROOT:-/}
SYSTEMCTL=${LOKI_SYSTEMCTL:-systemctl}
HEALTH_ATTEMPTS=${LOKI_HEALTH_ATTEMPTS:-100}
HEALTH_INTERVAL=${LOKI_HEALTH_INTERVAL:-0.2}
SUITE=loki-go.target
SERVICES="loki-go-signing-agent.service loki-go-port-guard.service loki-go-egress-proxy.service loki-go-runtime.service loki-go-browser-proxy.service loki-go-browser.service loki-go-mcp.service"

case "$ROOT" in /*) ;; *) echo "LOKI_INSTALL_ROOT must be absolute" >&2; exit 2 ;; esac
test "$(realpath -m "$ROOT")" = "$ROOT" || {
  echo "LOKI_INSTALL_ROOT must be a clean path" >&2
  exit 2
}

rooted() {
  if test "$ROOT" = /; then printf '%s\n' "$1"; else printf '%s%s\n' "$ROOT" "$1"; fi
}

fail() {
  echo "$*" >&2
  exit 1
}

valid_release() {
  case "$1" in
    ''|*[!A-Za-z0-9._-]*) return 1 ;;
  esac
}

atomic_link() {
  target=$1
  link=$2
  temporary="$link.new.$$"
  rm -f "$temporary"
  ln -s "$target" "$temporary"
  mv -Tf "$temporary" "$link"
}

require_link_or_absent() {
  path=$1
  test ! -e "$path" -a ! -L "$path" || test -L "$path" || fail "$path is occupied by an unmanaged installation"
}

preflight_integration() {
  require_link_or_absent "$(rooted /opt/loki)"
  require_link_or_absent "$(rooted /usr/local/bin/loki)"
  require_link_or_absent "$(rooted /usr/local/bin/devtools)"
  docs=$(rooted /usr/share/doc/loki)
  if test -e "$docs"; then
    test -d "$docs" -a ! -L "$docs" -a -f "$docs/.loki-go-managed" || fail "$docs is occupied by an unmanaged installation"
  fi
}

run_systemctl() {
  "$SYSTEMCTL" "$@"
}

acquire_lock() {
  base=$(rooted /opt/loki-go)
  install -d -m 0755 "$base"
  lock="$base/lifecycle.lock"
  exec 9>"$lock"
  flock 9
}

check_identity() {
  name=$1
  want=$2
  kind=$3
  if test "$kind" = group; then
    got=$(getent group "$name" 2>/dev/null | cut -d: -f3 || true)
  else
    got=$(getent passwd "$name" 2>/dev/null | cut -d: -f3 || true)
  fi
  test -z "$got" || test "$got" = "$want" || fail "$kind $name has id $got, expected $want"
}

check_id_available() {
  name=$1
  want=$2
  kind=$3
  if test "$kind" = group; then
    existing=$(getent group "$want" 2>/dev/null | cut -d: -f1 || true)
  else
    existing=$(getent passwd "$want" 2>/dev/null | cut -d: -f1 || true)
  fi
  test -z "$existing" -o "$existing" = "$name" || fail "$kind id $want belongs to $existing"
}

ensure_identities() {
  runner_uid=$1 runner_gid=$2 workspace_gid=$3 browser_uid=$4
  test "$ROOT" = / || return 0
  check_identity workspace "$workspace_gid" group
  check_identity runner "$runner_gid" group
  check_identity runner "$runner_uid" user
  check_identity loki-browser "$browser_uid" user
  check_id_available workspace "$workspace_gid" group
  check_id_available runner "$runner_gid" group
  check_id_available runner "$runner_uid" user
  check_id_available loki-browser "$browser_uid" user
  getent group workspace >/dev/null || groupadd --gid "$workspace_gid" workspace
  getent group runner >/dev/null || groupadd --gid "$runner_gid" runner
  getent passwd runner >/dev/null || useradd --uid "$runner_uid" --gid runner --groups workspace --home-dir /home/runner --create-home --shell /bin/bash runner
  if ! id -nG runner | tr ' ' '\n' | grep -qx workspace; then usermod -a -G workspace runner; fi
  getent passwd loki-browser >/dev/null || useradd --uid "$browser_uid" --gid workspace --home-dir /var/lib/loki-go/browser --no-create-home --shell /usr/sbin/nologin loki-browser
}

release_path() {
  rooted "/opt/loki-go/releases/$1"
}

read_link() {
  path=$1
  test -L "$path" && readlink "$path" || true
}

deploy_integration() {
  release=$1
  opt=$(rooted /opt/loki)
  docs=$(rooted /usr/share/doc/loki)
  current=$(rooted /opt/loki-go/current)
  loki_bin=$(rooted /usr/local/bin/loki)
  devtools_bin=$(rooted /usr/local/bin/devtools)
  lifecycle_bin=$(rooted /usr/local/sbin/loki-go-lifecycle)
  install -d "$(dirname "$opt")" "$(dirname "$docs")" "$(rooted /usr/local/bin)" "$(rooted /usr/local/sbin)" \
    "$(rooted /usr/lib/systemd/system)" "$(rooted /usr/lib/tmpfiles.d)"
  atomic_link "loki-go/current/opt/loki" "$opt"
  install -d -m 0755 "$docs"
  cp -a "$release/usr/share/doc/loki/." "$docs/"
  : > "$docs/.loki-go-managed"
  chmod 0644 "$docs/.loki-go-managed"
  atomic_link "../../../opt/loki/bin/loki" "$loki_bin"
  atomic_link "../../../opt/loki/libexec/devtools" "$devtools_bin"
  install -m 0755 "$release/opt/loki/libexec/lifecycle" "$lifecycle_bin"
  install -m 0644 "$release/usr/lib/systemd/system/"*.service "$release/usr/lib/systemd/system/"*.target "$(rooted /usr/lib/systemd/system/)"
  install -m 0644 "$release/usr/lib/tmpfiles.d/loki-go.conf" "$(rooted /usr/lib/tmpfiles.d/loki-go.conf)"
}

activate_release() {
  release=$1
  deploy_integration "$release"
  prepare_state "$release"
  run_systemctl daemon-reload
  run_systemctl enable "$SUITE"
  run_systemctl restart "$SUITE"
  health
}

prepare_state() {
  release=$1
  set -- $(cat "$release/opt/loki-go-identities")
  runner_uid=$1 runner_gid=$2 workspace_gid=$3 browser_uid=$4
  ensure_identities "$runner_uid" "$runner_gid" "$workspace_gid" "$browser_uid"
  config=$(rooted /etc/loki-go)
  install -d -g "$workspace_gid" -m 0750 "$config"
  test -f "$config/config.toml" || install -m 0640 "$release/usr/share/doc/loki/config.toml" "$config/config.toml"
  test -f "$config/gitconfig" || install -m 0644 "$release/usr/share/doc/loki/gitconfig" "$config/gitconfig"
  chgrp "$workspace_gid" "$config/config.toml"
  "$release/opt/loki/libexec/render-layouts" "$release/usr/share/doc/loki" "$config" "$runner_uid" "$runner_gid" "$workspace_gid" "$browser_uid"
  install -d -m 0700 "$(rooted /var/lib/loki-go/runtime/inbox)" "$(rooted /var/lib/loki-go/signing)" "$(rooted /var/lib/loki-go/browser)"
  install -d -o "$runner_uid" -g "$runner_gid" -m 0700 \
    "$(rooted /var/lib/loki-go/runner)" "$(rooted /var/lib/loki-go/runner-config)" "$(rooted /var/lib/loki-go/runner-gh-config)" \
    "$(rooted /var/lib/loki-go/runner-data)" "$(rooted /var/lib/loki-go/runner-xdg-state)" "$(rooted /var/lib/loki-go/snapshots)" \
    "$(rooted /var/cache/loki-go/runner)" "$(rooted /var/cache/loki-go/runner-npm)" "$(rooted /var/cache/loki-go/runner-pnpm)" \
    "$(rooted /var/cache/loki-go/runner-playwright)" "$(rooted /var/cache/loki-go/runner-go-build)" "$(rooted /var/cache/loki-go/runner-go-mod)" \
    "$(rooted /var/cache/loki-go/runner-pip)" "$(rooted /var/tmp/loki-go/runner)"
  install -d -m 0700 "$(rooted /var/log/loki-go/runtime)" "$(rooted /var/log/loki-go/mcp)"
  install -d -o "$runner_uid" -g "$workspace_gid" -m 2770 "$(rooted /srv/workspace/loki)"
  install -d -o "$browser_uid" -g "$workspace_gid" -m 0770 "$(rooted /srv/workspace/loki/.loki-go/browser-downloads)"
  install -d -g "$workspace_gid" -m 0755 "$(rooted /srv/workspace/loki/.agents/skills)"
  cp -a "$release/srv/workspace/loki/.agents/skills/." "$(rooted /srv/workspace/loki/.agents/skills/)"
  if test ! -f "$config/token"; then
    umask 0077
    dd if=/dev/urandom bs=48 count=1 2>/dev/null | base64 > "$config/token"
  fi
  chgrp "$workspace_gid" "$config/token"
  chmod 0640 "$config/token"
  key=$(rooted /var/lib/loki-go/signing/id_ed25519)
  test -f "$key" || ssh-keygen -q -t ed25519 -N "" -C "loki-go signing" -f "$key"
  prepare_signing_identity "$runner_uid" "$runner_gid" "$config" "$key"
}

prepare_signing_identity() {
  runner_uid=$1 runner_gid=$2 config=$3 key=$4
  test "$ROOT" = / || return 0
  identity_name=$(runuser -u runner -- env HOME=/home/runner git config --global --includes user.name || true)
  identity_email=$(runuser -u runner -- env HOME=/home/runner git config --global --includes user.email || true)
  test -n "$identity_name" -a -n "$identity_email" || fail "runner Git user.name and user.email must be configured before installing Loki Go"
  case "$identity_email" in *' '*|*'	'*|*'
'*) fail "runner Git user.email must not contain whitespace" ;; esac
  ssh_dir=$(rooted /home/runner/.ssh)
  install -d -o "$runner_uid" -g "$runner_gid" -m 0700 "$ssh_dir"
  install -o "$runner_uid" -g "$runner_gid" -m 0644 "$key.pub" "$ssh_dir/id_ed25519.pub"
  public_key=$(cat "$key.pub")
  printf '%s %s\n' "$identity_email" "$public_key" > "$config/allowed_signers"
  chown root:root "$config/allowed_signers"
  chmod 0644 "$config/allowed_signers"
}

health() {
  case "$HEALTH_ATTEMPTS" in ''|*[!0-9]*) fail "LOKI_HEALTH_ATTEMPTS must be a positive integer" ;; esac
  test "$HEALTH_ATTEMPTS" -gt 0 || fail "LOKI_HEALTH_ATTEMPTS must be positive"
  attempt=0
  while test "$attempt" -lt "$HEALTH_ATTEMPTS"; do
    active=yes
    for service in $SERVICES; do
      run_systemctl is-active --quiet "$service" || active=no
    done
    if test "$active" = yes && \
       test -S "$(rooted /run/loki-go/runtime/control.sock)" && \
       test -S "$(rooted /run/loki-go/port-guard/control.sock)" && \
       test -S "$(rooted /run/loki-go/browser/control.sock)" && \
       test -S "$(rooted /run/loki-go/signing/agent.sock)"; then
      return 0
    fi
    attempt=$((attempt + 1))
    sleep "$HEALTH_INTERVAL"
  done
  return 1
}

switch_to() {
  release_id=$1
  release=$(release_path "$release_id")
  test -d "$release" || fail "release $release_id is not installed"
  preflight_integration
  current=$(rooted /opt/loki-go/current)
  old=$(read_link "$current")
  atomic_link "releases/$release_id" "$current"
  if (activate_release "$release"); then
    if test -n "$old" -a "$old" != "releases/$release_id"; then
      atomic_link "$old" "$(rooted /opt/loki-go/previous)"
    fi
    return 0
  fi
  echo "release $release_id failed health check; restoring previous release" >&2
  if test -n "$old"; then
    atomic_link "$old" "$current"
    restored="$(rooted /opt/loki-go)/$old"
    (activate_release "$restored") || fail "previous release also failed health check"
  else
    rm -f "$current"
    run_systemctl disable --now "$SUITE" || true
  fi
  return 1
}

install_release() {
  test "$#" -eq 6 -o "$#" -eq 7 || fail "usage: $0 install ARTIFACT RELEASE RUNNER_UID RUNNER_GID WORKSPACE_GID BROWSER_UID [PYTHON_VAULT_COPY]"
  artifact=$1 release_id=$2
  valid_release "$release_id" || fail "invalid release id"
  case "$artifact" in /*) ;; *) fail "candidate artifact path must be absolute" ;; esac
  test "$(realpath -m "$artifact")" = "$artifact" || fail "candidate artifact path must be clean"
  for identity in "$3" "$4" "$5" "$6"; do
    case "$identity" in ''|*[!0-9]*) fail "service identities must be numeric" ;; esac
  done
  test -d "$artifact/rootfs" -a -f "$artifact/SHA256SUMS" || fail "candidate artifact is incomplete"
  if test "$ROOT" = /; then test "$(id -u)" -eq 0 || fail "installation requires root"; fi
  releases=$(rooted /opt/loki-go/releases)
  destination="$releases/$release_id"
  test ! -e "$destination" || fail "release $release_id is already installed"
  install -d -m 0755 "$releases"
  temporary="$releases/.$release_id.staging.$$"
  trap 'rm -rf "$temporary"' EXIT HUP INT TERM
  (
    cd "$artifact/rootfs"
    sha256sum -c ../SHA256SUMS
  )
  install -d -m 0755 "$temporary"
  cp -a --no-preserve=ownership "$artifact/rootfs/." "$temporary/"
  install_args=""
  if test "$ROOT" != / || test "${LOKI_SKIP_APT:-0}" = 1; then install_args=--skip-apt; fi
  "$temporary/opt/loki/bin/loki" toolchain install --bundle "$temporary/usr/share/loki/toolchain" --root "$temporary" $install_args
  printf '%s %s %s %s\n' "$3" "$4" "$5" "$6" > "$temporary/opt/loki-go-identities"
  mv "$temporary" "$destination"
  trap - EXIT HUP INT TERM
  if test "$#" -eq 7; then
    trap 'rm -rf "$destination"' EXIT HUP INT TERM
    install -d -m 0755 "$(rooted /var/lib/loki-go)"
    "$destination/opt/loki/bin/loki" migrate-vault import --source-copy "$7" --destination "$(rooted /var/lib/loki-go/runtime)"
    trap - EXIT HUP INT TERM
  fi
  switch_to "$release_id"
}

rollback() {
  current=$(rooted /opt/loki-go/current)
  previous=$(rooted /opt/loki-go/previous)
  old=$(read_link "$current")
  target=$(read_link "$previous")
  test -n "$old" -a -n "$target" || fail "no previous release is available"
  atomic_link "$target" "$current"
  release="$(rooted /opt/loki-go)/$target"
  if (activate_release "$release"); then
    atomic_link "$old" "$previous"
    return 0
  fi
  atomic_link "$old" "$current"
  release="$(rooted /opt/loki-go)/$old"
  (activate_release "$release") || fail "rollback and recovery both failed health checks"
  return 1
}

command=${1-}
case "$command" in
  install) acquire_lock; shift; install_release "$@" ;;
  activate) test "$#" -eq 2 || fail "usage: $0 activate RELEASE"; acquire_lock; switch_to "$2" ;;
  rollback) test "$#" -eq 1 || fail "usage: $0 rollback"; acquire_lock; rollback ;;
  health) test "$#" -eq 1 || fail "usage: $0 health"; health ;;
  *) fail "usage: $0 install|activate|rollback|health" ;;
esac
