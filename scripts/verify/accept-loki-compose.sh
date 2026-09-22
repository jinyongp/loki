#!/usr/bin/env bash
set -euo pipefail
umask 077

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
image=${LOKI_IMAGE:-loki:local}
browser_image=${LOKI_BROWSER_IMAGE:-}
docker=${LOKI_DOCKER:-docker}
root=$(mktemp -d "${TMPDIR:-/tmp}/loki-compose-acceptance.XXXXXX")
workspace=$root/workspace
token_file=$root/mcp-token
project=lokiaccept$$
derived_image=$project:derived
before=$root/invariants.before
after=$root/invariants.after

die() { printf 'loki-compose-acceptance: %s\n' "$*" >&2; exit 1; }

compose() {
  LOKI_IMAGE=$image \
  LOKI_JOB_IMAGE=$image \
  LOKI_BROWSER_IMAGE=${browser_image:-loki-browser:local} \
  LOKI_WORKSPACE=$workspace \
  LOKI_MCP_TOKEN_FILE=$token_file \
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
  "$docker" image rm "$derived_image" >/dev/null 2>&1 || true
  rm -rf -- "$root"
  exit "$result"
}
trap cleanup EXIT HUP INT TERM

command -v "$docker" >/dev/null || die "Docker is required"
"$docker" buildx version >/dev/null 2>&1 || die "Docker Buildx with BuildKit is required"
command -v setfacl >/dev/null || die "setfacl is required"
case $(uname -s) in Linux) ;; *) die "current acceptance target must be Linux or WSL2" ;; esac
if grep -qi microsoft /proc/sys/kernel/osrelease 2>/dev/null; then host=wsl2; else host=linux; fi

mkdir -p "$workspace"
setfacl -m u:10000:rwx,d:u:10000:rwx "$workspace"
token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
printf %s "$token" >"$token_file"
chmod 0444 "$token_file"

snapshot_invariants "$before"
compose config --quiet
compose up -d --remove-orphans
for service in egress launcher executor runtime mcp; do
  wait_healthy "$service" || die "$service is unhealthy"
done
services=$(compose ps --services --status running)
for service in egress launcher executor runtime mcp; do
  grep -qx "$service" <<<"$services" || die "$service is not running"
done
for service in browser browser-proxy signing; do
  if grep -qx "$service" <<<"$services"; then die "optional service started in the core profile: $service"; fi
done
compose exec -T --user 10000:10000 runtime /usr/local/bin/devtools version >/dev/null
assert_networks runtime "${project}_private"
assert_networks mcp "${project}_private"
assert_networks egress "$(printf '%s\n%s' "${project}_outbound" "${project}_private" | sort)"
assert_no_mount mcp /var/lib/loki/runtime
assert_no_mount executor /workspace
assert_no_mount executor /run/docker.sock
assert_not_inspectable mcp "$token"
assert_not_inspectable egress "$token"

compose restart
for service in egress launcher executor runtime mcp; do
  wait_healthy "$service" || die "$service is unhealthy after restart"
done

base_id=$("$docker" image inspect --format '{{.Id}}' "$image")
"$docker" buildx build --quiet --load \
  --build-arg "LOKI_BASE=$image" \
  --build-arg "LOKI_BASE_ID=$base_id" \
  --file "$repo/packaging/images/derived/Dockerfile" \
  --tag "$derived_image" "$repo" >/dev/null
"$repo/scripts/verify/verify-loki-derived-image.sh" "$image" "$derived_image"

if test -n "$browser_image"; then
  compose --profile browser up -d browser browser-proxy
  wait_healthy browser-proxy || die "browser proxy is unhealthy"
  wait_healthy browser || die "browser profile is unhealthy"
  assert_networks browser "${project}_private"
  assert_networks browser-proxy "$(printf '%s\n%s' "${project}_outbound" "${project}_private" | sort)"
  for service in browser browser-proxy; do
    assert_no_mount "$service" /workspace
    assert_no_mount "$service" /var/lib/loki/runtime
    assert_not_inspectable "$service" "$token"
  done
fi

if test -n "${LOKI_SIGNING_KEY_FILE:-}"; then
  test -f "$LOKI_SIGNING_KEY_FILE" || die "signing key does not exist"
  LOKI_SIGNING_KEY_FILE=$LOKI_SIGNING_KEY_FILE compose --profile signing up -d signing
  wait_healthy signing || die "signing profile is unhealthy"
  assert_no_mount signing /workspace
  assert_no_mount signing /var/lib/loki/runtime
  assert_not_inspectable signing "$token"
fi

snapshot_invariants "$after"
cmp "$before" "$after" || die "an invariant source or state path changed"
printf 'loki-compose-acceptance: passed topology/isolation smoke on %s\n' "$host"
