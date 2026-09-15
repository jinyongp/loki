#!/usr/bin/env bash
set -euo pipefail
umask 077

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
image=${LOKI_IMAGE:-loki:local}
browser_image=${LOKI_BROWSER_IMAGE:-}
docker=${LOKI_DOCKER:-docker}
root=$(mktemp -d "${TMPDIR:-/tmp}/loki-compose-acceptance.XXXXXX")
state=$root/state
workspace=$root/workspace
backup=$root/backup
project=lokiaccept$$
upgrade_image=$project:upgrade
before=$root/invariants.before
after=$root/invariants.after

die() { printf 'loki-compose-acceptance: %s\n' "$*" >&2; exit 1; }

compose() {
  LOKI_IMAGE=$image \
  LOKI_BROWSER_IMAGE=${browser_image:-loki-browser:local} \
  LOKI_WORKSPACE=$workspace \
  LOKI_MCP_TOKEN_FILE=$state/mcp-token \
    "$docker" compose --project-name "$project" --file "$repo/compose.yaml" "$@"
}

snapshot_invariants() {
  local output=$1 root_path
  : >"$output"
  while IFS= read -r root_path; do
    test -z "$root_path" && continue
    test -e "$root_path" || die "invariant path does not exist: $root_path"
    if test -d "$root_path"; then
      find "$root_path" -xdev -type f -print0 |
        sort -z |
        xargs -0 -r sha256sum >>"$output"
    else
      sha256sum "$root_path" >>"$output"
    fi
  done <<<"${LOKI_ACCEPTANCE_INVARIANT_PATHS:-}"
}

wait_healthy() {
  local service=$1 attempt id status
  for attempt in $(seq 1 60); do
    id=$(compose ps -q "$service")
    if test -n "$id"; then
      status=$("$docker" inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$id")
      test "$status" = healthy && return 0
    fi
    sleep 1
  done
  return 1
}

container_id() { compose ps -q "$1"; }

assert_networks() {
  local service=$1 expected=$2 actual
  actual=$("$docker" inspect --format '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' "$(container_id "$service")" | sed '/^$/d' | sort)
  test "$actual" = "$expected" || die "$service network isolation changed: $actual"
}

assert_no_mount() {
  local service=$1 destination=$2
  if "$docker" inspect --format '{{range .Mounts}}{{println .Destination}}{{end}}' "$(container_id "$service")" | grep -Fxq "$destination"; then
    die "$service received forbidden mount: $destination"
  fi
}

assert_not_inspectable() {
  local service=$1 sentinel=$2
  if "$docker" inspect "$(container_id "$service")" | grep -Fq -- "$sentinel"; then
    die "$service inspect data disclosed a credential sentinel"
  fi
}

cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if test "$result" -ne 0; then
    compose --profile browser --profile signing ps >&2 || true
    compose --profile browser --profile signing logs --tail 100 browser browser-proxy signing >&2 || true
  fi
  compose --profile browser --profile signing down --volumes --remove-orphans >/dev/null 2>&1 || true
  "$docker" image rm "$upgrade_image" >/dev/null 2>&1 || true
  rm -rf -- "$root"
  exit "$result"
}
trap cleanup EXIT HUP INT TERM

command -v "$docker" >/dev/null || die "Docker is required"
command -v setfacl >/dev/null || die "setfacl is required"
case $(uname -s) in Linux) ;; *) die "current acceptance target must be Linux or WSL2" ;; esac
if grep -qi microsoft /proc/sys/kernel/osrelease 2>/dev/null; then host=wsl2; else host=linux; fi

snapshot_invariants "$before"
token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
printf %s "$token" |
  LOKI_COMPOSE_PROJECT=$project LOKI_COMPOSE_STATE_DIR=$state LOKI_IMAGE=$image \
    "$repo/scripts/loki-compose-lifecycle.sh" initialize "$workspace" "$image"

life() {
  LOKI_COMPOSE_PROJECT=$project LOKI_COMPOSE_STATE_DIR=$state \
    "$repo/scripts/loki-compose-lifecycle.sh" "$@"
}

life install
life health
services=$(compose ps --services --status running)
for service in egress runtime mcp; do grep -qx "$service" <<<"$services" || die "$service is not running"; done
for service in browser browser-proxy signing; do
  if grep -qx "$service" <<<"$services"; then die "optional service started in the core profile: $service"; fi
done
compose exec -T --user 10000:10000 runtime /usr/local/bin/devtools version >/dev/null
assert_networks runtime "${project}_private"
assert_networks mcp "${project}_private"
assert_networks egress "$(printf '%s\n%s' "${project}_outbound" "${project}_private" | sort)"
assert_no_mount mcp /var/lib/loki/runtime
assert_not_inspectable mcp "$token"
assert_not_inspectable egress "$token"

life restart
life health
compose exec -T --user 10000:10000 mcp sh -ec 'printf "runner-owned\n" > /var/lib/loki/runner/restore-owner-check; chmod 0600 /var/lib/loki/runner/restore-owner-check'
life backup "$backup"
life restore "$backup"
compose exec -T --user 10000:10000 mcp sh -ec 'test "$(cat /var/lib/loki/runner/restore-owner-check)" = runner-owned; test "$(stat -c %u:%g /var/lib/loki/runner/restore-owner-check)" = 10000:10000; rm /var/lib/loki/runner/restore-owner-check'
rotated_token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
printf %s "$rotated_token" | life rotate-credentials
assert_not_inspectable mcp "$token"
assert_not_inspectable mcp "$rotated_token"
assert_not_inspectable egress "$token"
assert_not_inspectable egress "$rotated_token"

"$docker" tag "$image" "$upgrade_image"
life upgrade "$upgrade_image"
test "$(cat "$state/current-image")" = "$upgrade_image" || die "upgrade image was not selected"
life rollback
test "$(cat "$state/current-image")" = "$image" || die "rollback image was not restored"

base_id=$("$docker" image inspect --format '{{.Id}}' "$image")
"$docker" build --quiet \
  --build-arg "LOKI_BASE=$image" \
  --build-arg "LOKI_BASE_ID=$base_id" \
  --file "$repo/packaging/container/derived/Dockerfile" \
  --tag "$upgrade_image" "$repo" >/dev/null
"$repo/scripts/verify-loki-derived-image.sh" "$image" "$upgrade_image"

if test -n "$browser_image"; then
  compose --profile browser up -d browser
  wait_healthy browser || die "browser profile is unhealthy"
  assert_networks browser "${project}_private"
  assert_networks browser-proxy "$(printf '%s\n%s' "${project}_outbound" "${project}_private" | sort)"
  for service in browser browser-proxy; do
    assert_no_mount "$service" /workspace
    assert_no_mount "$service" /var/lib/loki/runtime
    assert_not_inspectable "$service" "$rotated_token"
  done
  life health
fi

if test -n "${LOKI_SIGNING_KEY_FILE:-}"; then
  test -f "$LOKI_SIGNING_KEY_FILE" || die "signing key does not exist"
  LOKI_SIGNING_KEY_FILE=$LOKI_SIGNING_KEY_FILE compose --profile signing up -d signing
  wait_healthy signing || die "signing profile is unhealthy"
  life health
fi

snapshot_invariants "$after"
cmp "$before" "$after" || die "an invariant source or state path changed"
printf 'loki-compose-acceptance: passed on %s\n' "$host"
