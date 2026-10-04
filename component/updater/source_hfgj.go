package updater

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Set only in HFGJ builds with -ldflags -X. Empty values retain upstream sources.
// A configured build never falls back to upstream when a channel is unavailable.
var (
	HFGJReleaseBaseURL string
	HFGJAlphaBaseURL   string
)

type hfgjCoreSource struct {
	baseURL    string
	versionURL string
	custom     bool
}

func hfgjCoreUpdateSource(channel, currentVersion, releaseURL, alphaURL string) (hfgjCoreSource, error) {
	useAlpha := false
	switch strings.ToLower(channel) {
	case ReleaseChannel:
	case AlphaChannel:
		useAlpha = true
	default: // Keep upstream auto-channel semantics.
		useAlpha = strings.HasPrefix(currentVersion, "alpha")
	}
	if releaseURL == "" && alphaURL == "" {
		if useAlpha {
			return hfgjCoreSource{baseURL: baseAlphaURL, versionURL: versionAlphaURL}, nil
		}
		return hfgjCoreSource{baseURL: baseReleaseURL, versionURL: versionReleaseURL}, nil
	}
	base := releaseURL
	if useAlpha {
		base = alphaURL
	}
	if base == "" {
		return hfgjCoreSource{}, fmt.Errorf("HFGJ update channel is not configured")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return hfgjCoreSource{}, fmt.Errorf("HFGJ update source must be an HTTPS base URL without credentials, query or fragment")
	}
	base = strings.TrimRight(u.String(), "/") + "/"
	return hfgjCoreSource{baseURL: base, versionURL: base + "version.txt", custom: true}, nil
}

// Keep release tokens compatible with the HFGJ Verge managed-core updater.
var hfgjVersionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,63}$`)

// A custom version becomes part of a local filename and a download URL.
func hfgjValidateUpdateVersion(version string) error {
	if !hfgjVersionName.MatchString(version) {
		return fmt.Errorf("invalid HFGJ update version")
	}
	return nil
}

func hfgjCorePackageName(base, version, goos string) string {
	ext := ".gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return base + "-" + version + ext
}
