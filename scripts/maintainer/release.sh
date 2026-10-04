#!/bin/sh
set -eu

usage() {
  cat <<'EOF'
usage: sh ./scripts/maintainer/release.sh [--version 0.2.x] [--validate-only] [--force-rebuild] [--oci]

Runs the complete local source + race preflight before starting release CI.
--validate-only  Run candidate CI without publishing a release.
--force-rebuild Build every supported target in CI.
--oci            Also run the local real OCI acceptance fixture.
EOF
}

version=''
publish=true
force_rebuild=false
oci=0
while test "$#" -gt 0; do
  case "$1" in
    --validate-only) publish=false ;;
    --force-rebuild) force_rebuild=true ;;
    --version) shift; test "$#" -gt 0 || { usage >&2; exit 2; }; version=$1 ;;
    --oci) oci=1 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
  shift
done

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
cd "$repo"
command -v gh >/dev/null 2>&1 || { echo 'release requires GitHub CLI (gh)' >&2; exit 1; }

require_source() {
  test "$(git symbolic-ref --short HEAD)" = main || {
    echo 'release requires the main branch' >&2; return 1;
  }
  test -z "$(git status --porcelain --untracked-files=normal)" || {
    echo 'release requires a clean checkout; commit and push the intended changes first' >&2; return 1;
  }
  test "$(git rev-parse HEAD)" = "$(git ls-remote --exit-code origin refs/heads/main | cut -f1)" || {
    echo 'release requires HEAD to match origin/main' >&2; return 1;
  }
}

require_source
source_sha=$(git rev-parse HEAD)
printf 'loki release: source=%s state=validating\n' "$source_sha"
if test "$oci" -eq 1; then
  sh "$repo/scripts/verify/verify-preflight.sh" --oci
else
  sh "$repo/scripts/verify/verify-preflight.sh"
fi

# Validation can take minutes. Dispatch only the unchanged, published source.
require_source
test "$(git rev-parse HEAD)" = "$source_sha" || {
  echo 'release source changed during validation; rerun the release command' >&2; exit 1;
}
printf 'loki release: source=%s state=dispatching publish=%s\n' "$source_sha" "$publish"
gh workflow run release.yml --ref main \
  -f "version=$version" -f "publish=$publish" -f "force_rebuild=$force_rebuild" -f "expected_sha=$source_sha"
printf 'loki release: source=%s state=dispatched remaining=CI-acceptance,publication\n' "$source_sha"
