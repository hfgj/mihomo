package sing_tun

import "testing"

func TestHFGJShouldDisableSingTunDNSHijack(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		dnsHijack []string
		want      bool
	}{
		{name: "windows empty hijack", goos: "windows", dnsHijack: nil, want: true},
		{name: "windows explicit hijack", goos: "windows", dnsHijack: []string{"any:53"}, want: false},
		{name: "linux empty hijack", goos: "linux", dnsHijack: nil, want: false},
		{name: "darwin empty hijack", goos: "darwin", dnsHijack: nil, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldDisableSingTunDNSHijack(tc.goos, tc.dnsHijack); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
