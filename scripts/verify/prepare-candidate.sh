#!/bin/sh
set -eu

if test "$#" -ne 1; then
  echo "usage: prepare-candidate.sh CANDIDATE_DIRECTORY" >&2
  exit 2
fi

candidate=$(realpath "$1")
test -d "$candidate" || {
  echo "loki candidate preparation: directory does not exist: $candidate" >&2
  exit 1
}

if test -f "$candidate/install.sh"; then
  chmod 0755 "$candidate/install.sh"
fi

for relative in \
  inputs/loki \
  inputs/loki-bootstrap \
  inputs/loki-windows-amd64.exe
do
  path="$candidate/$relative"
  test -f "$path" || {
    echo "loki candidate preparation: missing $relative" >&2
    exit 1
  }
  chmod 0755 "$path"
done

printf 'loki candidate preparation: executable modes restored\n'
