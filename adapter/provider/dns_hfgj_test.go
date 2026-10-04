package provider

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/metacubex/mihomo/common/yaml"
	"github.com/metacubex/mihomo/component/geodata"
	"github.com/metacubex/mihomo/component/geodata/router"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/component/trie"
	C "github.com/metacubex/mihomo/constant"
	D "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestHFGJProviderHosts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		hosts       map[string]any
		query, want string
		fail        bool
	}{
		{"chain", map[string]any{"a.example": "b.example", "b.example": "127.0.0.1"}, "A.EXAMPLE.", "127.0.0.1", false},
		{"wildcard", map[string]any{"+.example": "b.test", "b.test": "127.0.0.2"}, "node.example", "127.0.0.2", false},
		{"cycle", map[string]any{"a.example": "b.example", "b.example": "a.example"}, "a.example", "", true},
		{"wildcard-cycle", map[string]any{"+.example": "b.example"}, "a.example", "", true},
		{"invalid-target", map[string]any{"a.example": "b.*.test"}, "a.example", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newHFGJProviderResolver(tc.hosts, hfgjProviderDNS{})
			if tc.fail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			ips, err := r.LookupIPv4(context.Background(), tc.query)
			require.NoError(t, err)
			require.Equal(t, tc.want, ips[0].String())
		})
	}
	r, err := newHFGJProviderResolver(nil, hfgjProviderDNS{})
	require.NoError(t, err)
	require.Nil(t, r)
	off := false
	r, err = newHFGJProviderResolver(map[string]any{"a.example": "a.example"}, hfgjProviderDNS{UseHosts: &off})
	require.NoError(t, err)
	require.Nil(t, r)
}

func TestHFGJProviderScopeAndFallback(t *testing.T) {
	oldHosts, oldResolver := resolver.DefaultHosts, resolver.ProxyServerHostResolver
	t.Cleanup(func() { resolver.DefaultHosts = oldHosts; resolver.ProxyServerHostResolver = oldResolver })
	globals := trie.New[resolver.HostValue]()
	hv, err := resolver.NewHostValue([]string{"127.0.0.99"})
	require.NoError(t, err)
	require.NoError(t, globals.Insert("a.example", hv))
	resolver.DefaultHosts = resolver.NewHosts(globals)
	fake := &hfgjRecordingResolver{}
	resolver.ProxyServerHostResolver = fake
	r, err := newHFGJProviderResolver(map[string]any{"a.example": "b.example"}, hfgjProviderDNS{})
	require.NoError(t, err)
	ips, err := resolver.LookupIPv4WithResolver(context.Background(), "a.example", r)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.7", ips[0].String())
	require.Equal(t, "b.example", fake.host)
	// Ordinary/global lookups retain upstream global-host precedence.
	ips, err = resolver.LookupIPv4WithResolver(context.Background(), "a.example", fake)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.99", ips[0].String())
	require.Same(t, fake, resolver.ProxyServerHostResolver)
}

type hfgjRecordingResolver struct{ host string }

func (r *hfgjRecordingResolver) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	r.host = host
	return []netip.Addr{netip.MustParseAddr("127.0.0.7")}, nil
}
func (r *hfgjRecordingResolver) LookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.LookupIP(ctx, host)
}
func (r *hfgjRecordingResolver) LookupIPv6(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.LookupIP(ctx, host)
}
func (r *hfgjRecordingResolver) ResolveECH(context.Context, string) ([]byte, error) { return nil, nil }
func (r *hfgjRecordingResolver) ExchangeContext(context.Context, *D.Msg) (*D.Msg, error) {
	return nil, nil
}
func (r *hfgjRecordingResolver) Invalid() bool    { return true }
func (r *hfgjRecordingResolver) ClearCache()      {}
func (r *hfgjRecordingResolver) ResetConnection() {}

func TestHFGJSelfDNS(t *testing.T) {
	for _, tc := range []struct {
		server, listen string
		want           bool
	}{
		{"udp://127.0.0.1:7874", "127.0.0.1:7874", true},
		{"tcp://127.0.0.1:7874", "0.0.0.0:7874", true},
		{"127.0.0.1", "0.0.0.0:53", true},
		{"udp://[::1]:53", "[::]:53", true},
		{"udp://127.0.0.1:53", "[::]:53", false},
		{"udp://127.0.0.2:53", "127.0.0.1:53", false},
		{"udp://127.0.0.1:1053", "127.0.0.1:7874", false},
		{"tls://127.0.0.1:7874", "127.0.0.1:7874", false},
		{"udp://127.0.0.1:7874#DIRECT", "127.0.0.1:7874", false},
	} {
		require.Equal(t, tc.want, hfgjSelfDNS(tc.server, tc.listen), "%s / %s", tc.server, tc.listen)
	}
}

func TestHFGJProviderDNSValidation(t *testing.T) {
	// The external integration test imports config, which registers the upstream
	// DNS URI parser before any test runs.
	for _, body := range []string{
		"listen: 127.0.0.1:7874\nproxy-server-nameserver: [udp://127.0.0.1:7874]\nnameserver: [udp://127.0.0.1:7874]",
		"listen: 127.0.0.1:7874\nproxy-server-nameserver: [udp://127.0.0.1:7874, 1.1.1.1]\nnameserver: [1.1.1.1]",
		"listen: 127.0.0.1:7874\nproxy-server-nameserver: [udp://127.0.0.1:7874]\nnameserver: [1.1.1.1]\nfallback: [8.8.8.8]",
		"proxy-server-nameserver: [not-a-scheme://test]\n",
		"proxy-server-nameserver: [1.1.1.1]\ndefault-nameserver: [tls://seed.example]",
		"proxy-server-nameserver: [1.1.1.1]\nproxy-server-nameserver-policy:\n  'geosite:cn': [8.8.8.8]",
	} {
		var cfg hfgjProviderDNS
		require.NoError(t, yaml.Unmarshal([]byte(body), &cfg))
		_, err := newHFGJProviderResolver(nil, cfg)
		require.Error(t, err)
	}
	// Ordinary global DNS alone is not silently promoted to node DNS.
	r, err := newHFGJProviderResolver(nil, hfgjProviderDNS{NameServer: []string{"1.1.1.1"}})
	require.NoError(t, err)
	require.Nil(t, r)
}

// Opt-in static check: private inputs stay outside Git, test output reports only
// counts. No upstream lookups or node connections are performed here.
func TestHFGJPrivateProviderSamples(t *testing.T) {
	dir := os.Getenv("HFGJ_PROVIDER_SAMPLE_DIR")
	if dir == "" {
		t.Skip("private provider samples not requested")
	}
	oldHome, oldLoader := C.Path.HomeDir(), geodata.LoaderName()
	C.SetHomeDir(t.TempDir())
	geodata.SetLoader("standard")
	geodata.ClearGeoSiteCache()
	t.Cleanup(func() { geodata.ClearGeoSiteCache(); geodata.SetLoader(oldLoader); C.SetHomeDir(oldHome) })
	data, err := proto.Marshal(&router.GeoSiteList{Entry: []*router.GeoSite{{CountryCode: "PRIVATE", Domain: []*router.Domain{{Type: router.Domain_Full, Value: "fixture.example"}}}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(C.Path.GeoSite(), data, 0600))
	// Synthetic GeoSite data permits construction without using public services.
	parser, err := NewProxiesParser("sample", nil, "", "", "", "", overrideSchema{}, "")
	require.NoError(t, err)
	for _, name := range []string{"amy", "ytoo", "lc", "tag"} {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(dir, name+".yaml"))
			if err != nil {
				t.Fatal("cannot read local sample")
			}
			proxies, err := parser(body)
			if err != nil {
				t.Fatal("local sample failed provider parsing (details withheld)")
			}
			t.Logf("parsed %d nodes", len(proxies))
			for _, proxy := range proxies {
				proxy.Close()
			}
		})
	}
}

func TestHFGJProviderFilterOverrideAndPureNodes(t *testing.T) {
	yes, iface, mark, prefix := true, "retained-interface", 123, "pref-"
	parser, err := NewProxiesParser("identity-only", nil, "^wanted", "skip", "trojan", "", overrideSchema{TFO: &yes, MPTcp: &yes, Interface: &iface, RoutingMark: &mark, AdditionalPrefix: &prefix}, "")
	require.NoError(t, err)
	body := []byte(`hosts: {a.example: b.example}
dns: {proxy-server-nameserver: [1.1.1.1], default-nameserver: [system]}
proxies:
  - {name: wanted, type: ss, server: a.example, port: 443, cipher: aes-128-gcm, password: test-only}
  - {name: wanted-skip, type: ss, server: a.example, port: 443, cipher: aes-128-gcm, password: test-only}
  - {name: wanted-excluded, type: trojan, server: a.example, port: 443, password: test-only}
  - {name: ignored, type: ss, server: a.example, port: 443, cipher: aes-128-gcm, password: test-only}
`)
	nodes, err := parser(body)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Equal(t, "pref-wanted", nodes[0].Name())
	require.Equal(t, "a.example:443", nodes[0].Addr())
	info := nodes[0].ProxyInfo()
	require.True(t, info.TFO)
	require.True(t, info.MPTCP)
	require.Equal(t, iface, info.Interface)
	require.Equal(t, mark, info.RoutingMark)
	require.Equal(t, "identity-only", info.ProviderName)
	plain, err := NewProxiesParser("plain", nil, "", "", "", "", overrideSchema{}, "")
	require.NoError(t, err)
	// No metadata: an adapter outside scoped endpoint support retains upstream behavior.
	nodes, err = plain([]byte("proxies: [{name: plain, type: direct}]"))
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	_, err = plain([]byte("hosts: {a.example: b.example}\nproxies: [{name: plain, type: direct}]"))
	require.ErrorContains(t, err, "unsupported node protocol")
}
