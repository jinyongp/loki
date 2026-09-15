#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
file=${LOKI_COMPOSE_FILE:-$repo/compose.yaml}
state=${LOKI_COMPOSE_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/loki-compose}
project=${LOKI_COMPOSE_PROJECT:-loki}
docker=${LOKI_DOCKER:-docker}
tries=${LOKI_HEALTH_ATTEMPTS:-30}
pause=${LOKI_HEALTH_INTERVAL:-2}
die(){ printf 'loki-compose: %s\n' "$*" >&2; exit 1; }
dc(){ "$docker" "$@"; }
need(){ local n; for n in workspace current-image mcp-token; do test -f "$state/$n"||die "not initialized; run initialize first"; done; }
compose(){ LOKI_IMAGE=$(cat "$state/current-image") LOKI_WORKSPACE=$(cat "$state/workspace") LOKI_MCP_TOKEN_FILE=$state/mcp-token dc compose --project-name "$project" --file "$file" "$@"; }
lock(){ mkdir -p "$state"; chmod 0700 "$state"; mkdir "$state/.lock" 2>/dev/null||die "another lifecycle operation is running"; trap 'rm -rf "$state/.lock"' EXIT INT TERM; }
put(){ local d=$1 v=$2 t; test -n "$v"||die "empty state value"; case $v in *$'\n'*) die "state values must be one line";; esac; t=$(mktemp "$state/.write.XXXXXX"); printf '%s\n' "$v">"$t"; chmod 0600 "$t"; mv -f "$t" "$d"; }
token(){ local t bytes last; t=$(mktemp "$state/.token.XXXXXX"); cat>"$t"; bytes=$(wc -c<"$t"|tr -d ' '); test "$bytes" -ge 43||{ rm -f "$t"; die "token must contain at least 256 bits of entropy"; }; test "$bytes" -le 4096||{ rm -f "$t"; die "token exceeds 4096 bytes"; }; last=$(tail -c 1 "$t"|od -An -tuC|tr -d ' '); case $last in 10|13) rm -f "$t"; die "token must not end with a newline";; esac; chmod 0444 "$t"; mv -f "$t" "$state/mcp-token"; }
image_ok(){ local i=$1 title; test -n "$i"||die "empty image"; case $i in *$'\n'*) die "invalid image";; esac; title=$(dc image inspect --format '{{ index .Config.Labels "org.opencontainers.image.title" }}' "$i" 2>/dev/null)||die "image unavailable: $i"; test "$title" = Loki||die "not a Loki image: $i"; }
prepare_workspace(){ local w=$1 created=$3; test "$created" = 1||return 0; command -v setfacl>/dev/null||die "new workspace needs POSIX ACL support"; setfacl -m u:10000:rwx,d:u:10000:rwx "$w"; }
preflight(){ need; case $project in ''|*[!a-z0-9_-]*) die "invalid project name";; esac; test -f "$file"||die "Compose file missing"; dc compose version>/dev/null; dc info>/dev/null; image_ok "$(cat "$state/current-image")"; test -d "$(cat "$state/workspace")"||die "workspace missing"; compose config --quiet; printf 'loki-compose: preflight passed\n'; }
healthy(){ local try svc id status ok; for ((try=1;try<=tries;try++)); do ok=1; for svc in egress runtime mcp; do id=$(compose ps -q "$svc"); test -n "$id"||{ ok=0; break; }; status=$(dc inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$id" 2>/dev/null||true); test "$status" = healthy||{ ok=0; break; }; done; test "$ok" = 1&&return; sleep "$pause"; done; return 1; }
start(){ compose up -d --remove-orphans&&healthy; }
running(){ test -n "$(compose ps --status running -q 2>/dev/null)"; }
vol(){ printf '%s_%s\n' "$project" "$1"; }
hash(){ if command -v sha256sum>/dev/null; then sha256sum "$1"|awk '{print $1}'; else shasum -a 256 "$1"|awk '{print $1}'; fi; }
archive(){ local n=$1 dir=$2 i v; i=$(cat "$state/current-image"); v=$(vol "$n"); dc volume inspect "$v">/dev/null&&dc run --rm --network none --read-only --cap-drop ALL --cap-add DAC_READ_SEARCH --cap-add CHOWN --security-opt no-new-privileges --entrypoint /bin/sh --mount "type=volume,src=$v,dst=/source,readonly" --mount "type=bind,src=$dir,dst=/backup" "$i" -ec 'cd /source&&tar -cf "/backup/$1.tar" .&&chown "$2:$3" "/backup/$1.tar"' sh "$n" "$(id -u)" "$(id -g)"; }
backup(){ local dst=$1 parent tmp active=0 n digest; test ! -e "$dst"||die "backup destination exists"; parent=$(dirname "$dst"); mkdir -p "$parent"; tmp=$(mktemp -d "$parent/.loki-backup.XXXXXX"); chmod 0733 "$tmp"; running&&active=1; if ! compose stop --timeout 30 >/dev/null; then rm -rf "$tmp"; return 1; fi; for n in runtime-state runner-state; do if ! archive "$n" "$tmp"; then rm -rf "$tmp"; test "$active" = 0||start||true; return 1; fi; done; chmod 0700 "$tmp"; { printf 'format=1\nimage=%s\n' "$(cat "$state/current-image")"; for n in runtime-state runner-state; do digest=$(hash "$tmp/$n.tar"); printf '%s=%s\n' "$n" "$digest"; done; }>"$tmp/manifest"; if ! chmod -R go-rwx "$tmp"; then rm -rf "$tmp"; test "$active" = 0||start||true; return 1; fi; mv "$tmp" "$dst"; test "$active" = 0||start; }
verify(){ local dir=$1 n expected; test "$(sed -n 's/^format=//p' "$dir/manifest" 2>/dev/null)" = 1||return 1; for n in runtime-state runner-state; do test -f "$dir/$n.tar"||return 1; expected=$(sed -n "s/^$n=//p" "$dir/manifest"); test -n "$expected"&&test "$(hash "$dir/$n.tar")" = "$expected"||return 1; done; }
unpack(){ local n=$1 dir=$2 i v; i=$(cat "$state/current-image"); v=$(vol "$n"); dc volume create "$v">/dev/null; dc run --rm --network none --cap-drop ALL --cap-add DAC_OVERRIDE --cap-add FOWNER --cap-add CHOWN --security-opt no-new-privileges --entrypoint /bin/sh --mount "type=volume,src=$v,dst=/target" --mount "type=bind,src=$dir,dst=/backup,readonly" "$i" -ec 'find /target -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +&&tar -xf "/backup/$1.tar" -C /target' sh "$n"; }
restore_payload(){ local dir=$1 n; verify "$dir"||return 1; compose down --remove-orphans>/dev/null||return 1; for n in runtime-state runner-state; do unpack "$n" "$dir"||return 1; done; start; }
stamp(){ date -u +%Y%m%dT%H%M%SZ; }
initialize(){ local w=$1 i=${2:-loki:local} created=0; case $w in /*);;*) die "workspace must be absolute";; esac; test ! -e "$state/workspace"||die "already initialized"; if test ! -e "$w"; then mkdir -p "$w"; created=1; fi; test -d "$w"||die "workspace is not a directory"; mkdir -p "$state/backups"; token; image_ok "$i"; prepare_workspace "$w" "$i" "$created"; put "$state/workspace" "$w"; put "$state/current-image" "$i"; preflight; printf 'loki-compose: initialized\n'; }
restore(){ local src=$1 safety="$state/backups/pre-restore-$(stamp)"; verify "$src"||die "backup verification failed"; backup "$safety"||die "safety backup failed"; if ! restore_payload "$src"; then restore_payload "$safety"||true; die "restore failed; prior state recovered"; fi; printf 'loki-compose: restored\n'; }
upgrade(){ local next=$1 old snap; image_ok "$next"; old=$(cat "$state/current-image"); test "$next" != "$old"||die "image unchanged"; snap="$state/backups/upgrade-$(stamp)"; backup "$snap"||die "upgrade backup failed"; put "$state/previous-image" "$old"; put "$state/current-image" "$next"; put "$state/rollback-backup" "$snap"; if ! compose up -d --force-recreate --remove-orphans||! healthy; then put "$state/current-image" "$old"; restore_payload "$snap"||true; die "upgrade failed; previous release restored"; fi; printf 'loki-compose: upgraded\n'; }
rollback(){ local old current snap; test -f "$state/previous-image"&&test -f "$state/rollback-backup"||die "no rollback recorded"; old=$(cat "$state/previous-image"); current=$(cat "$state/current-image"); snap=$(cat "$state/rollback-backup"); image_ok "$old"; put "$state/current-image" "$old"; if ! restore_payload "$snap"; then put "$state/current-image" "$current"; start||true; die "rollback failed"; fi; rm -f "$state/previous-image" "$state/rollback-backup"; printf 'loki-compose: rolled back\n'; }
rotate(){ local old; old=$(mktemp "$state/.old-token.XXXXXX"); cp "$state/mcp-token" "$old"; token; if ! compose up -d --force-recreate mcp egress||! healthy; then chmod 0444 "$old"; mv -f "$old" "$state/mcp-token"; compose up -d --force-recreate mcp egress>/dev/null||true; die "rotation failed; previous token restored"; fi; rm -f "$old"; printf 'loki-compose: credential rotated\n'; }
usage(){ printf '%s\n' 'usage: loki-compose-lifecycle.sh initialize WORKSPACE [IMAGE] | preflight | install | health | restart | backup DIR | restore DIR | upgrade IMAGE | rollback | rotate-credentials'; }
cmd=${1:-}; shift||true
case $cmd in
 initialize) test "$#" -ge 1&&test "$#" -le 2||die "initialize requires WORKSPACE and optional IMAGE"; lock; initialize "$@";;
 preflight) preflight;; install) lock; preflight; start||die "services unhealthy";; health) need; healthy||die "services unhealthy";;
 restart) lock; need; compose restart; healthy||die "services unhealthy";;
 backup) test "$#" = 1||die "backup requires DIR"; lock; need; backup "$1"||die "backup failed"; printf '%s\n' "$1";;
 restore) test "$#" = 1||die "restore requires DIR"; lock; need; restore "$1";;
 upgrade) test "$#" = 1||die "upgrade requires IMAGE"; lock; need; upgrade "$1";;
 rollback) lock; need; rollback;; rotate-credentials) lock; need; rotate;;
 help|-h|--help) usage;; *) usage>&2; exit 2;;
esac
