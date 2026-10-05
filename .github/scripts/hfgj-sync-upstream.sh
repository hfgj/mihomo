#!/usr/bin/env bash
set -euo pipefail
test "$#" -gt 0
case "$1" in prepare|promote) ;; *) echo 'Usage: hfgj-sync-upstream.sh prepare|promote [arguments]' >&2; exit 1 ;; esac
exec python3 "$(dirname "$0")/hfgj-release.py" "$@"
