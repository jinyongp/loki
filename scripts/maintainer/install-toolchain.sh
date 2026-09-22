#!/bin/sh
set -eu

if test "$#" -ne 2; then
  echo "usage: install-toolchain.sh LOKI_BINARY TOOLCHAIN_BUNDLE" >&2
  exit 2
fi
test "$(id -u)" -eq 0 || { echo "toolchain installation requires root" >&2; exit 1; }
test -x "$1" || { echo "Loki binary is not executable" >&2; exit 1; }
case "$2" in /*) ;; *) echo "toolchain bundle must be absolute" >&2; exit 2 ;; esac

exec "$1" toolchain install --bundle "$2" --root /
