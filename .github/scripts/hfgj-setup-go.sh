#!/usr/bin/env bash
# Versioned HFGJ snapshot of the MetaCubeX archive; never follow its rolling build tag.
# See .github/toolchains/README.md for provenance and the upgrade procedure.
set -euo pipefail
test -n "$RUNNER_TEMP"
archive="$RUNNER_TEMP/hfgj-go1.26.10.linux-amd64.tar.gz"
curl --fail --location --retry 3 --output "$archive" \
  https://github.com/hfgj/mihomo/releases/download/toolchain-go1.26.10-c1278af2579c/go1.26.10.linux-amd64.tar.gz
printf '%s  %s\n' c1278af2579c9398cf22c638686e5348926b160eda784930d416d95aaa7a9221 "$archive" | sha256sum -c -
mkdir -p "$RUNNER_TEMP/hfgj-go"
tar -xzf "$archive" -C "$RUNNER_TEMP/hfgj-go"
go_bin="$RUNNER_TEMP/hfgj-go/go/bin"
"$go_bin/go" version
printf '%s\n' "$go_bin" >> "$GITHUB_PATH"
