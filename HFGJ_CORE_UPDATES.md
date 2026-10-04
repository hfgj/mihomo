# HFGJ core updates and packages

This stable-branch patch extends the existing core updater with build-time
source selection. Provider DNS remains a separate patch documented in
`HFGJ_PROVIDER_DNS.md`. The upstream mirror branches and the existing Windows
TUN DNS helper are unchanged.

## Source selection

Two string variables in `component/updater` may be set with Go linker flags:

```
-X github.com/metacubex/mihomo/component/updater.HFGJReleaseBaseURL=https://github.com/hfgj/mihomo/releases/download/HFGJ-Stable/
-X github.com/metacubex/mihomo/component/updater.HFGJAlphaBaseURL=https://github.com/hfgj/mihomo/releases/download/HFGJ-Alpha/
```

They are build settings, not new YAML fields or API parameters. Without either
flag, the upstream stable/Alpha selection and URLs remain unchanged. With a
custom source, the selected channel must be configured; a missing channel
returns an error before downloading or touching the installed executable. There
is no fallback to the official core that would silently remove HFGJ patches.
Only HTTPS base URLs without credentials, query strings or fragments are used.

The current `hfgj` workflow injects **only HFGJ-Stable**. Alpha has not yet been
ported or validated. Choosing Alpha from this stable core therefore fails
explicitly. The workflow retains the existing Alpha branch case for a future
reviewed port; changing the workflow on `hfgj` does not change the file on
`hfgj-alpha`, and does not constitute an Alpha release.

The source provides `version.txt` and versioned asset names. Version tokens are
plain ASCII alphanumeric/dot/hyphen/underscore, at most 64 characters. Stable
CI versions look like `v1.19.31-hfgj.<12-character-patch-commit>`. `constant.Version`,
`version.txt` and all package suffixes agree, so patch-only updates are detected
by both Mihomo and the HFGJ Verge managed-core updater. Local dirty builds use a
distinct `-hfgj.local.<source-fingerprint>` suffix and are not published releases.

The existing download, unpack, backup, replacement and API restart machinery is
retained. This patch only changes source selection and custom version checks;
it does not strengthen the upstream replacement/rollback algorithm or add
checksum verification at runtime. `SHA256SUMS` is published for manual checks.
An installed official core cannot discover these flags: the first HFGJ core
must still be installed through a package or a separately approved replacement.

## Release assets

| Target | Unversioned executable | Release archive |
| --- | --- | --- |
| Linux ARM64/v8.0 | `mihomo-linux-arm64` | `mihomo-linux-arm64-<version>.gz` |
| Windows amd64/v2 | `mihomo-windows-amd64-v2.exe` | `mihomo-windows-amd64-v2-<version>.zip` |
| Windows ARM64/v8.0 | `mihomo-windows-arm64.exe` | `mihomo-windows-arm64-<version>.zip` |
| macOS ARM64/v8.0 | `mihomo-darwin-arm64` | `mihomo-darwin-arm64-<version>.gz` |
| macOS amd64/v1 | `mihomo-darwin-amd64-v1` | `mihomo-darwin-amd64-v1-<version>.gz` |

The workflow file keeps its historical name `.github/workflows/hfgj-windows.yml`
to avoid creating a second publishing workflow. Builds use MetaCubeX Go 1.26,
`CGO_ENABLED=0` and `with_gvisor`. Alpha portability and older macOS compatibility
need separate validation; these Go 1.26 assets are not the upstream `go122` assets.

`.github/scripts/hfgj-package-core.py` creates the archives. The ZIP's only entry
is the unversioned `.exe`; GZIP's header contains the unversioned executable
name. Omitting the gzip filename (for example using `gzip -n`) breaks the existing
Mihomo unpacker's expected path. Packages must be smaller than its 32 MiB limit.
CI publishes the five packages, `version.txt` and `SHA256SUMS` together only after
all matrix jobs succeed. Pushing the relevant patch branches can automatically
replace the rolling release; pushing is therefore a publishing operation.

## Client boundaries

Zashboard's core upgrade uses the Mihomo API, so an installed HFGJ build selects
its injected source. Whether replacement/restart succeeds under Nikki/procd,
permissions and the real configuration still requires device testing.

The public `hfgj/clash-verge-rev:hfgj` sources examined on 2026-10-04 already
select HFGJ packages for Windows x64/ARM64 in `scripts/prebuild.mjs` and
`src-tauri/src/feat/core_upgrade.rs`. Their package names and version parser are
compatible with these Windows assets. This was a source review, not a desktop
application build or an application update test.

That Verge fork still selects upstream packages for macOS. Its own downloader
does not use Mihomo's injected variables. A separate small client patch must
extend HFGJ selection to macOS and change its macOS asset map to the names above
in both prebuild and the app updater. Merely publishing macOS assets does not
change what the existing app downloads. No Verge files were changed here.

OpenWrt `.ipk`/`.apk` packages and feeds are also separate from these raw archives.
Nikki depends on virtual package `mihomo`; its official installer explicitly
installs `mihomo-meta` from the official feed. A future HFGJ package/installer can
satisfy that dependency without modifying Nikki's runtime or LuCI interface.
The package must use the correct firmware/architecture and preserve alternatives
and file ownership. No OpenWrt package feed or installer is published by this
workflow.

## Verification

Tests cover source/channel selection, unavailable-channel preservation of the
installed file, version-token validation, linker injection and existing archive
unpacking. Run the tests along with provider DNS and Windows TUN regressions:

```
go test -race -tags with_gvisor ./adapter/provider ./adapter/outbound ./component/resolver ./component/resource ./component/updater ./config ./dns ./listener/sing_tun -count=1
go test -tags with_gvisor ./component/updater -run TestHFGJLinkedCoreSource -count=1 -ldflags "-X github.com/metacubex/mihomo/component/updater.HFGJReleaseBaseURL=https://github.com/hfgj/mihomo/releases/download/HFGJ-Stable/"
```

Local validation, cross-compilation and archive checks do not confirm live GitHub
Actions, an actual release download, Nikki restart, desktop app updates, or
airport connectivity. Complete those checks after separately approved deployment.
