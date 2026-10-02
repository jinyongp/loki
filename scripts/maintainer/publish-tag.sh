#!/bin/sh
set -eu

if test "$#" -ne 2; then
  echo 'usage: publish-tag.sh TAG ACCEPTED_COMMIT' >&2
  exit 2
fi
tag=$1
commit=$2
ref="refs/tags/$tag"
git check-ref-format "$ref"
test "$(git rev-parse --verify "$commit^{commit}")" = "$commit" || {
  echo 'release tag requires the full accepted commit SHA' >&2; exit 1;
}
delay=${LOKI_TAG_RETRY_DELAY_SECONDS:-5}
case "$delay" in
  ''|*[!0-9]*) echo 'invalid tag retry delay' >&2; exit 2 ;;
esac
test "$delay" -le 60 || { echo 'tag retry delay exceeds 60 seconds' >&2; exit 2; }

LC_ALL=C
export LC_ALL
scratch=$(mktemp -d)
trap 'rm -f "$scratch/remote" "$scratch/error" "$scratch/push"; rmdir "$scratch"' EXIT
trap 'exit 1' HUP INT TERM

transient() {
  # Authentication, tag conflicts and policy rejection are permanent failures.
  if grep -Eiq 'Authentication failed|Permission denied|Repository not found|requested URL returned error: (401|403)|protected tag|pre-receive hook declined|would clobber existing tag|\[rejected\]' "$1"; then
    return 1
  fi
  grep -Eiq 'remote: fatal error in commit_refs|The requested URL returned error: (408|429|5[0-9][0-9])|Could not resolve host|Connection timed out|Operation timed out|Connection reset by peer|Failed to connect' "$1"
}

read_remote() {
  lookup=1
  while ! git ls-remote --refs origin "$ref" >"$scratch/remote" 2>"$scratch/error"; do
    cat "$scratch/error" >&2
    if test "$lookup" -ge 3 || ! transient "$scratch/error"; then
      echo 'release tag remote verification failed' >&2
      exit 1
    fi
    echo "Retrying remote tag lookup after transient failure ($lookup/3)..." >&2
    sleep "$((delay * lookup))"
    lookup=$((lookup + 1))
  done
  remote=$(cut -f1 "$scratch/remote")
  if test -n "$remote" && test "$remote" != "$commit"; then
    echo "release tag $tag already points to a different commit; refusing to replace it" >&2
    exit 1
  fi
}

read_remote
test -z "$remote" || exit 0
if local_commit=$(git show-ref --verify --hash "$ref"); then
  test "$local_commit" = "$commit" || {
    echo 'local release tag points to a different commit' >&2; exit 1;
  }
else
  git tag --no-sign "$tag" "$commit"
fi

attempt=1
while test "$attempt" -le 3; do
  # Reconcile an ambiguous prior push before making another write.
  read_remote
  test -z "$remote" || exit 0
  if git push origin "$ref" >"$scratch/push" 2>&1; then
    cat "$scratch/push"
    read_remote
    test "$remote" = "$commit" || {
      echo 'successful tag push did not publish the accepted commit' >&2; exit 1;
    }
    exit 0
  fi
  cat "$scratch/push" >&2
  read_remote
  test -z "$remote" || exit 0
  if test "$attempt" -ge 3 || ! transient "$scratch/push"; then
    echo 'release tag publication failed' >&2
    exit 1
  fi
  echo "Retrying tag publication after transient failure ($attempt/3)..." >&2
  sleep "$((delay * attempt))"
  attempt=$((attempt + 1))
done
