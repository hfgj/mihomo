# HFGJ Go toolchain snapshots

HFGJ verification and all five core builds use `.github/scripts/hfgj-setup-go.sh`.
The installer downloads a versioned snapshot from this repository and verifies
its committed SHA-256 before extracting or executing it. It does not fall back
to MetaCubeX's rolling `build` release or to a different Go toolchain.

## Current snapshot

- Toolchain: MetaCubeX patched Go `go1.26.10`, Linux amd64.
- Release: [`toolchain-go1.26.10-c1278af2579c`](https://github.com/hfgj/mihomo/releases/tag/toolchain-go1.26.10-c1278af2579c).
- Archive: `go1.26.10.linux-amd64.tar.gz` (67,770,554 bytes).
- SHA-256: `c1278af2579c9398cf22c638686e5348926b160eda784930d416d95aaa7a9221`.
- Original upstream release: [`MetaCubeX/go`, tag `build`](https://github.com/MetaCubeX/go/releases/tag/build).
- Original asset: `go1.26.linux-amd64.tar.gz`, asset ID `662923077`,
  uploaded on 2026-10-21. The snapshot is an unchanged copy of those bytes.

The snapshot release also contains `SHA256SUMS` and `provenance.json`, including
the original asset URL, GitHub asset identity and digest. The archive retains
the upstream source and `go/LICENSE`.

The toolchain release is marked as a prerelease and is not the repository's
latest release. It is separate from the `HFGJ-Stable` core update channel.

## Upgrade procedure

1. Download the intended official MetaCubeX archive. Check its SHA-256 against
   GitHub's asset digest and inspect `go/VERSION` to confirm the exact version.
2. Create a **new** snapshot tag containing the full Go version and the first
   twelve characters of its SHA-256, using the naming above. Upload the unchanged
   archive under its full versioned filename, plus `SHA256SUMS` and provenance.
   Publish it as a prerelease with `latest=false`.
3. Download the published snapshot anonymously and verify its SHA-256 again.
   Only then update the installer URL, archive filename and committed checksum
   together, and update this record.
4. Run the maintenance script tests and the full HFGJ verification/build workflow
   before promoting a core release.

Never move a published snapshot tag, replace its assets or use `--clobber`.
Retain old snapshots so historical commits remain buildable. An upstream rebuild
with different bytes requires a new snapshot even when the Go version is the
same. Keep checksum verification enabled; a mismatch must stop installation.
