package outbound

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	D "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

type hfgjDialCapture struct{ network, address string }

func (d *hfgjDialCapture) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	d.network = network
	d.address = address
	return nil, nil
}
func (d *hfgjDialCapture) ListenPacket(context.Context, string, string, netip.AddrPort) (net.PacketConn, error) {
	return nil, nil
}

type hfgjDualResolver struct{}

func (r hfgjDualResolver) LookupIP(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")}, nil
}
func (r hfgjDualResolver) LookupIPv4(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
}
func (r hfgjDualResolver) LookupIPv6(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("2001:db8::1")}, nil
}
func (r hfgjDualResolver) ResolveECH(context.Context, string) ([]byte, error)      { return nil, nil }
func (r hfgjDualResolver) ExchangeContext(context.Context, *D.Msg) (*D.Msg, error) { return nil, nil }
func (r hfgjDualResolver) Invalid() bool                                           { return true }
func (r hfgjDualResolver) ClearCache()                                             {}
func (r hfgjDualResolver) ResetConnection()                                        {}
func (r hfgjDualResolver) IsScopedResolver() bool                                  { return true }

func TestHFGJProviderDialerFamiliesAndAPI(t *testing.T) {
	old := resolver.DisableIPv6
	resolver.DisableIPv6 = false
	t.Cleanup(func() { resolver.DisableIPv6 = old })
	for _, tc := range []struct {
		network string
		prefer  C.DNSPrefer
		want    string
	}{
		{"tcp", C.IPv4Only, "192.0.2.1:443"},
		{"tcp", C.IPv6Prefer, "[2001:db8::1]:443"},
		{"tcp4", C.IPv6Only, "192.0.2.1:443"},
		{"tcp6", C.IPv4Only, "[2001:db8::1]:443"},
	} {
		capture := &hfgjDialCapture{}
		d := newHFGJProviderDialer(capture, hfgjDualResolver{}, tc.prefer)
		_, err := d.DialContext(context.Background(), tc.network, "node.example:443")
		require.NoError(t, err)
		require.Equal(t, tc.want, capture.address)
		require.Equal(t, tc.network, capture.network)
	}
	capture := &hfgjDialCapture{}
	opt := BasicOption{DialerForAPI: capture, ProviderResolver: hfgjDualResolver{}, TFO: true, Interface: "retained", RoutingMark: 123, IPVersion: C.IPv4Only}
	// Upstream API dialer precedence is retained; it is not replaced by provider DNS.
	require.Same(t, capture, opt.NewDialer(nil))
	addr, err := resolveUDPAddr(context.Background(), "udp", "node.example:443", C.IPv6Only, hfgjDualResolver{})
	require.NoError(t, err)
	require.Equal(t, "[2001:db8::1]:443", addr.String())
}
