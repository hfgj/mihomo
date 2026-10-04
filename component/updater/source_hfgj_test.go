package updater

import (
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHFGJCoreUpdateSources(t *testing.T) {
	for _, tc := range []struct {
		name, channel, version, release, alpha, want string
		custom, fail                                 bool
	}{
		{"upstream-stable-auto", "", "v1.19.31", "", "", baseReleaseURL, false, false},
		{"upstream-alpha-auto", "auto", "alpha-abcdef", "", "", baseAlphaURL, false, false},
		{"upstream-release-selection", "RELEASE", "alpha-abcdef", "", "", baseReleaseURL, false, false},
		{"upstream-alpha-selection", "alpha", "v1.19.31", "", "", baseAlphaURL, false, false},
		{"hfgj-stable-auto", "auto", "v1.19.31-hfgj.abcdef", "https://example.com/HFGJ-Stable", "", "https://example.com/HFGJ-Stable/", true, false},
		{"hfgj-alpha-auto", "", "alpha-abcdef-hfgj.123456", "https://example.com/stable/", "https://example.com/alpha/", "https://example.com/alpha/", true, false},
		{"hfgj-release-selection", "release", "alpha-abcdef", "https://example.com/stable/", "https://example.com/alpha/", "https://example.com/stable/", true, false},
		{"missing-alpha-no-upstream-fallback", "alpha", "v1.19.31-hfgj.abcdef", "https://example.com/stable/", "", "", false, true},
		{"missing-release-no-upstream-fallback", "release", "alpha-abcdef", "", "https://example.com/alpha/", "", false, true},
		{"reject-http", "release", "v1.19.31", "http://example.com/", "", "", false, true},
		{"reject-credentials", "release", "v1.19.31", "https://user:pass@example.com/", "", "", false, true},
		{"reject-query", "release", "v1.19.31", "https://example.com/?token=test", "", "", false, true},
		{"reject-fragment", "release", "v1.19.31", "https://example.com/#test", "", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := hfgjCoreUpdateSource(tc.channel, tc.version, tc.release, tc.alpha)
			if tc.fail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, source.baseURL)
			require.Equal(t, tc.want+"version.txt", source.versionURL)
			require.Equal(t, tc.custom, source.custom)
		})
	}
}

func TestHFGJCoreUpdateVersions(t *testing.T) {
	for _, version := range []string{"v1.19.31-hfgj.123456789abc", "alpha-abcdef-hfgj.123456789abc"} {
		require.NoError(t, hfgjValidateUpdateVersion(version))
	}
	for _, version := range []string{"", "../core", "v1/../../core", "v1?test", "v1#test", "v1\nother", ".", "..", "\\core", "v1+build", strings.Repeat("v", 65)} {
		require.Error(t, hfgjValidateUpdateVersion(version))
	}
}

func TestHFGJMissingChannelLeavesInstalledCore(t *testing.T) {
	release, alpha := HFGJReleaseBaseURL, HFGJAlphaBaseURL
	t.Cleanup(func() { HFGJReleaseBaseURL, HFGJAlphaBaseURL = release, alpha })
	HFGJReleaseBaseURL, HFGJAlphaBaseURL = "https://example.com/stable/", ""
	dir := t.TempDir()
	exe := filepath.Join(dir, "mihomo")
	require.NoError(t, os.WriteFile(exe, []byte("installed-core"), 0755))
	require.ErrorContains(t, (&CoreUpdater{}).Update(exe, AlphaChannel, true), "channel is not configured")
	data, err := os.ReadFile(exe)
	require.NoError(t, err)
	require.Equal(t, "installed-core", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// Used with the same -X flags as release builds to catch linker-path mistakes.
func TestHFGJLinkedCoreSource(t *testing.T) {
	if HFGJReleaseBaseURL == "" && HFGJAlphaBaseURL == "" {
		t.Skip("upstream build has no injected HFGJ source")
	}
	version, base := "v1.19.31-hfgj.test", HFGJReleaseBaseURL
	if base == "" {
		version, base = "alpha-abcdef-hfgj.test", HFGJAlphaBaseURL
	}
	source, err := hfgjCoreUpdateSource("auto", version, HFGJReleaseBaseURL, HFGJAlphaBaseURL)
	require.NoError(t, err)
	require.True(t, source.custom)
	require.Equal(t, strings.TrimRight(base, "/")+"/version.txt", source.versionURL)
}

// Published archive payload names must agree with the existing updater. Gzip
// must carry the unversioned executable name in its header, not the asset name.
func TestHFGJCoreUpdateArchives(t *testing.T) {
	for _, tc := range []struct{ goos, base string }{
		{"linux", "mihomo-linux-arm64"},
		{"darwin", "mihomo-darwin-arm64"},
		{"darwin", "mihomo-darwin-amd64-v1"},
		{"windows", "mihomo-windows-amd64-v2"},
		{"windows", "mihomo-windows-arm64"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			dir := t.TempDir()
			version := "v1.19.31-hfgj.123456789abc"
			asset := filepath.Join(dir, hfgjCorePackageName(tc.base, version, tc.goos))
			f, err := os.Create(asset)
			require.NoError(t, err)
			payloadName := tc.base
			if tc.goos == "windows" {
				payloadName += ".exe"
				w := zip.NewWriter(f)
				entry, err := w.Create(payloadName)
				require.NoError(t, err)
				_, err = entry.Write([]byte("synthetic-core"))
				require.NoError(t, err)
				require.NoError(t, w.Close())
			} else {
				w := gzip.NewWriter(f)
				w.Name = payloadName
				_, err = w.Write([]byte("synthetic-core"))
				require.NoError(t, err)
				require.NoError(t, w.Close())
			}
			require.NoError(t, f.Close())
			out := filepath.Join(dir, "unpacked")
			require.NoError(t, os.Mkdir(out, 0700))
			require.NoError(t, (&CoreUpdater{}).unpack(out, asset, 0755))
			data, err := os.ReadFile(filepath.Join(out, payloadName))
			require.NoError(t, err)
			require.Equal(t, "synthetic-core", string(data))
		})
	}
}
