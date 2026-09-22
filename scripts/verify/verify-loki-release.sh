#!/bin/sh
set -eu

if test "$#" -ne 3; then
  echo "usage: verify-loki-release.sh CANDIDATE CORE_IMAGE BROWSER_IMAGE" >&2
  exit 2
fi

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
candidate=$(realpath "$1")
core_image=$2
browser_image=$3
docker=${LOKI_DOCKER:-docker}

test -x "$candidate/install.sh" || { echo "candidate install script is missing" >&2; exit 1; }

inspect_image() {
  image=$1
  title=$2
  test "$("$docker" image inspect --format '{{index .Config.Labels "org.opencontainers.image.title"}}' "$image")" = "$title"
  test "$("$docker" image inspect --format '{{.Os}}' "$image")" = linux
  case $("$docker" image inspect --format '{{.Architecture}}' "$image") in amd64|arm64) ;; *) return 1 ;; esac
}

inspect_image "$core_image" Loki
inspect_image "$browser_image" 'Loki Browser'

go test ./...
go test -race ./...
go vet ./...

"$repo/scripts/verify/accept-loki-bootstrap.sh"
"$repo/scripts/verify/accept-loki-go-candidate.sh" "$candidate"
LOKI_IMAGE=$core_image LOKI_BROWSER_IMAGE=$browser_image \
  "$repo/scripts/verify/accept-loki-compose.sh"

printf 'loki release verification: passed\n'
