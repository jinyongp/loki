#!/bin/sh
set -eu

if test "$#" -ne 1; then
  echo "usage: build-loki-toolchain-bundle.sh OUTPUT_DIRECTORY" >&2
  exit 2
fi

SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "$1" in
  /*) ;;
  *) echo "output directory must be absolute" >&2; exit 2 ;;
esac

exec go run "$SOURCE_DIR/internal/toolchain/cmd/fetch" \
  --manifest "$SOURCE_DIR/packaging/go/toolchain-manifest.json" \
  --output "$1"
