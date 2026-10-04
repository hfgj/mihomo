package outbound

import (
	"context"
	"net"
	"net/netip"
	"strings"

	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
)

// Resolve only the upstream node address before handing it to dialer-proxy.
// TLS/SNI and socket policy still belong to the original node/dialer.
type hfgjProviderDialer struct {
	C.Dialer
	resolver resolver.Resolver
	prefer   C.DNSPrefer
}

func newHFGJProviderDialer(d C.Dialer, r resolver.Resolver, prefer C.DNSPrefer) C.Dialer {
	return &hfgjProviderDialer{Dialer: d, resolver: r, prefer: prefer}
}
func (d *hfgjProviderDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return d.Dialer.DialContext(ctx, network, address)
	}
	prefer := d.prefer
	if strings.HasSuffix(network, "4") {
		prefer = C.IPv4Only
	} else if strings.HasSuffix(network, "6") {
		prefer = C.IPv6Only
	}
	ip, err := resolveIPWithResolver(ctx, host, prefer, d.resolver)
	if err != nil {
		return nil, err
	}
	return d.Dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
}
