#!/bin/sh
set -eu

if test "$#" -ne 1; then
  echo "usage: build-browser-oci.sh OUTPUT_OR_REF" >&2
  exit 2
fi

source_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
output=$1
case "$output" in
  /*) output_mode=archive ;;
  [a-z0-9]*/*:*) output_mode=registry ;;
  *) echo "output must be an absolute OCI archive path or registry tag" >&2; exit 2 ;;
esac

loki_version=${LOKI_VERSION:-0.49.0-dev}
loki_revision=${LOKI_REVISION:-$(git -C "$source_dir" rev-parse HEAD)}
loki_date=${LOKI_DATE:-$(git -C "$source_dir" show -s --format=%cI "$loki_revision" 2>/dev/null || printf unknown)}

set -- docker buildx build "$source_dir" \
  --file "$source_dir/packaging/images/browser.Dockerfile" \
  --platform linux/amd64,linux/arm64 \
  --build-arg "LOKI_VERSION=$loki_version" \
  --build-arg "LOKI_REVISION=$loki_revision" \
  --build-arg "LOKI_DATE=$loki_date" \
  --provenance=mode=max

if test -n "${LOKI_BUILD_CACHE_SCOPE:-}"; then
  set -- "$@" \
    --cache-from "type=gha,version=2,scope=$LOKI_BUILD_CACHE_SCOPE" \
    --cache-to "type=gha,version=2,mode=max,scope=$LOKI_BUILD_CACHE_SCOPE,ignore-error=true"
fi

if test "$output_mode" = archive; then
  set -- "$@" --output "type=oci,dest=$output"
else
  set -- "$@" --tag "$output" --push
fi
if test -n "${LOKI_BUILD_METADATA:-}"; then
  set -- "$@" --metadata-file "$LOKI_BUILD_METADATA"
fi
"$@"
