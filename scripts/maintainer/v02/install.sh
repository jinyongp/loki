#!/bin/sh
set -eu

bundle_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
bin_directory=${1:-"$HOME/.local/bin"}
management_root=${2:-}
if [ "$#" -gt 2 ]; then
  echo 'Usage: sh install.sh [absolute-bin-directory] [absolute-management-root]' >&2
  exit 2
fi
case "$bin_directory" in
  /*) ;;
  *) echo 'The bin directory must be an absolute path.' >&2; exit 2 ;;
esac
if [ -n "$management_root" ]; then
  "$bundle_directory/loki" --root "$management_root" install --bin-dir "$bin_directory"
else
  "$bundle_directory/loki" install --bin-dir "$bin_directory"
fi
