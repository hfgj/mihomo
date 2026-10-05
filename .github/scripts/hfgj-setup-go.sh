#!/usr/bin/env bash
# Fixed official MetaCubeX archive; fail rather than silently changing toolchains.
set -euo pipefail
test -n "$RUNNER_TEMP"
archive="$RUNNER_TEMP/hfgj-go1.26.linux-amd64.tar.gz"
curl --fail --location --retry 3 --output "$archive" \
  https://github.com/MetaCubeX/go/releases/download/build/go1.26.linux-amd64.tar.gz
printf '%s  %s\n' f7576103455a8882b3fa058aa552d76fed4ac66ed9ae6386801db0dfcf748243 "$archive" | sha256sum -c -
mkdir -p "$RUNNER_TEMP/hfgj-go"
tar -xzf "$archive" -C "$RUNNER_TEMP/hfgj-go"
go_bin="$RUNNER_TEMP/hfgj-go/go/bin"
"$go_bin/go" version
printf '%s\n' "$go_bin" >> "$GITHUB_PATH"
