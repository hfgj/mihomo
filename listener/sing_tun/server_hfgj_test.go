package sing_tun

import (
	"testing"

	tun "github.com/metacubex/sing-tun"
)

func TestHFGJSingTunDNSMode(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		dnsHijack []string
		mode      string
		want      string
	}{
		{name: "windows nil hijack", goos: "windows", dnsHijack: nil, want: tun.DNSModeDisabled},
		{name: "windows empty hijack", goos: "windows", dnsHijack: []string{}, want: tun.DNSModeDisabled},
		{name: "windows explicit hijack", goos: "windows", dnsHijack: []string{"any:53"}, want: ""},
		{name: "windows explicit disabled mode", goos: "windows", dnsHijack: []string{"any:53"}, mode: tun.DNSModeDisabled, want: tun.DNSModeDisabled},
		{name: "windows explicit native mode", goos: "windows", mode: tun.DNSModeNative, want: tun.DNSModeNative},
		{name: "windows explicit hijack mode", goos: "windows", mode: tun.DNSModeHijack, want: tun.DNSModeHijack},
		{name: "linux empty hijack", goos: "linux", dnsHijack: nil, want: ""},
		{name: "linux explicit hijack", goos: "linux", dnsHijack: []string{"any:53"}, want: ""},
		{name: "darwin empty hijack", goos: "darwin", dnsHijack: nil, want: ""},
		{name: "darwin explicit hijack", goos: "darwin", dnsHijack: []string{"any:53"}, want: ""},
		{name: "darwin explicit native mode", goos: "darwin", mode: tun.DNSModeNative, want: tun.DNSModeNative},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, autoRoute := range []bool{false, true} {
				options := tun.Options{AutoRoute: autoRoute, DNSMode: tc.mode}
				applySingTunDNSMode(tc.goos, tc.dnsHijack, &options)
				if options.DNSMode != tc.want {
					t.Fatalf("auto-route=%v: got mode %q, want %q", autoRoute, options.DNSMode, tc.want)
				}
				defaults := tun.Options{AutoRoute: autoRoute}
				want := tc.want
				if want == "" {
					want = defaults.DNSModeOrDefault()
				}
				if got := options.DNSModeOrDefault(); got != want {
					t.Fatalf("auto-route=%v: got %q, want %q", autoRoute, got, want)
				}
			}
		})
	}
}
