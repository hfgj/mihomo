package provider

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/metacubex/mihomo/common/utils"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/component/trie"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
	D "github.com/miekg/dns"
	"go.yaml.in/yaml/v3"
)

// Only node-resolution metadata is consumed. Airport routing, fake-IP pools and
// DNS listeners are deliberately not instantiated as part of a provider.
type hfgjProviderDNS struct {
	Enable         *bool     `yaml:"enable"`
	UseHosts       *bool     `yaml:"use-hosts"`
	Listen         string    `yaml:"listen"`
	NameServer     []string  `yaml:"nameserver"`
	Default        []string  `yaml:"default-nameserver"`
	ProxyServer    []string  `yaml:"proxy-server-nameserver"`
	Policy         yaml.Node `yaml:"nameserver-policy"`
	ProxyPolicy    yaml.Node `yaml:"proxy-server-nameserver-policy"`
	IPv6           bool      `yaml:"ipv6"`
	IPv6Timeout    uint      `yaml:"ipv6-timeout"`
	PreferH3       bool      `yaml:"prefer-h3"`
	RespectRules   bool      `yaml:"respect-rules"`
	CacheAlgorithm string    `yaml:"cache-algorithm"`
	CacheMaxSize   int       `yaml:"cache-max-size"`
	Fallback       []string  `yaml:"fallback"`
}

type hfgjProviderResolver struct {
	hosts   *trie.DomainTrie[resolver.HostValue]
	backend resolver.Resolver
	routes  []hfgjDNSRoute
	// Owned clients are scoped to this generation; old nodes retain old clients.
	owned []resolver.Resolver
}

var _ resolver.ScopedResolver = (*hfgjProviderResolver)(nil)

func newHFGJProviderResolver(hosts map[string]any, cfg hfgjProviderDNS) (resolver.Resolver, error) {
	if cfg.Enable != nil && !*cfg.Enable {
		cfg.ProxyServer = nil
		cfg.ProxyPolicy = yaml.Node{}
	}
	r := &hfgjProviderResolver{hosts: trie.New[resolver.HostValue]()}
	useHosts := cfg.UseHosts == nil || *cfg.UseHosts
	if useHosts {
		for domain, value := range hosts {
			values, err := utils.ToStringSlice(value)
			if err != nil {
				return nil, errors.New("invalid provider hosts value")
			}
			hv, err := resolver.NewHostValue(values)
			if err != nil {
				return nil, errors.New("invalid provider hosts target")
			}
			if hv.IsDomain {
				if _, err = trie.ValidAndSplitDomain(hv.Domain); err != nil || strings.ContainsAny(hv.Domain, "*+") {
					return nil, errors.New("invalid provider alias target")
				}
			}
			if err = r.hosts.Insert(domain, hv); err != nil {
				return nil, errors.New("invalid provider hosts key")
			}
		}
		// Validate all alias chains, including wildcard self-reference, before publish.
		for domain := range hosts {
			if _, _, err := r.host(domain); err != nil {
				return nil, err
			}
		}
	}
	explicit := len(cfg.ProxyServer) > 0 || len(cfg.ProxyPolicy.Content) > 0
	if (!useHosts || len(hosts) == 0) && !explicit {
		return nil, nil
	}
	if explicit {
		if dns.ParseNameServerWithOptions == nil {
			return nil, errors.New("DNS parser is not initialized")
		}
		if cfg.CacheMaxSize < 0 || (cfg.CacheAlgorithm != "" && cfg.CacheAlgorithm != "lru" && cfg.CacheAlgorithm != "arc") {
			return nil, errors.New("unsupported provider DNS cache settings")
		}
		// No network requests are made while preparing a provider generation.
		builder := &hfgjDNSBuilder{cfg: cfg, owner: r, cache: map[string]resolver.Resolver{}}
		var err error
		if len(cfg.ProxyServer) > 0 {
			r.backend, err = builder.nodeBackend(cfg.ProxyServer)
			if err != nil {
				return nil, err
			}
		}
		if err := eachHFGJPolicy(cfg.ProxyPolicy, func(domain string, servers []string) error {
			backend, err := builder.nodeBackend(servers)
			if err != nil {
				return err
			}
			return r.addRoute(domain, backend)
		}); err != nil {
			return nil, err
		}
	}
	r.hosts.Optimize()
	for _, route := range r.routes {
		if route.hosts != nil {
			route.hosts.Optimize()
		}
	}
	return r, nil
}

type hfgjDNSBuilder struct {
	cfg   hfgjProviderDNS
	owner *hfgjProviderResolver
	cache map[string]resolver.Resolver
	inner resolver.Resolver
}

func (b *hfgjDNSBuilder) parse(servers []string, respect bool) ([]dns.NameServer, error) {
	nss, err := dns.ParseNameServerWithOptions(servers, respect, b.cfg.PreferH3)
	if err != nil {
		return nil, errors.New("invalid provider DNS upstream")
	}
	if len(nss) == 0 {
		return nil, errors.New("provider DNS upstream is empty")
	}
	return nss, nil
}

// Match only an ordinary UDP/TCP loopback endpoint of this response's listener.
// Other local resolvers (different port, encrypted or parameterized) stay external.
func hfgjSelfDNS(server, listen string) bool {
	lh, lp, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if !strings.Contains(server, "://") {
		server = "udp://" + server
	}
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "udp" && u.Scheme != "tcp") || u.Fragment != "" || u.RawQuery != "" || u.Path != "" || u.User != nil {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "53"
	}
	if port != lp {
		return false
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() {
		return false
	}
	lip, err := netip.ParseAddr(lh)
	if lh == "" {
		return true
	}
	if err != nil {
		return false
	}
	if lip.IsUnspecified() {
		return lip.Is4() == ip.Is4()
	}
	return lip.Unmap() == ip.Unmap()
}

func (b *hfgjDNSBuilder) config(main []string, policy []dns.Policy, respect bool) (dns.Config, error) {
	nss, err := b.parse(main, respect)
	if err != nil {
		return dns.Config{}, err
	}
	seeds := b.cfg.Default
	if len(seeds) == 0 {
		seeds = []string{"system"}
	}
	for _, seed := range seeds {
		if hfgjSelfDNS(seed, b.cfg.Listen) {
			return dns.Config{}, errors.New("recursive provider DNS bootstrap")
		}
	}
	defaults, err := b.parse(seeds, false)
	if err != nil {
		return dns.Config{}, err
	}
	for _, ns := range defaults {
		if ns.Net == "system" {
			continue
		}
		addr := ns.Addr
		if ns.Net == "https" {
			u, err := url.Parse(addr)
			if err != nil {
				return dns.Config{}, errors.New("invalid DNS bootstrap")
			}
			addr = u.Host
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return dns.Config{}, errors.New("DNS bootstrap must use IP endpoints or system")
		}
		if _, err := netip.ParseAddr(host); err != nil {
			return dns.Config{}, errors.New("DNS bootstrap must use IP endpoints or system")
		}
	}
	return dns.Config{Main: nss, Default: defaults, Policy: policy, IPv6: b.cfg.IPv6, IPv6Timeout: b.cfg.IPv6Timeout, CacheAlgorithm: b.cfg.CacheAlgorithm, CacheMaxSize: b.cfg.CacheMaxSize}, nil
}

func (b *hfgjDNSBuilder) nodeBackend(servers []string) (resolver.Resolver, error) {
	key := strings.Join(servers, "\x00")
	if r, ok := b.cache[key]; ok {
		return r, nil
	}
	self := 0
	for _, server := range servers {
		if hfgjSelfDNS(server, b.cfg.Listen) {
			self++
		}
	}
	if self > 0 {
		if self != len(servers) {
			return nil, errors.New("mixed self-referencing and external node DNS is unsupported")
		}
		if b.cfg.Enable != nil && !*b.cfg.Enable {
			return nil, errors.New("self-referencing DNS is disabled")
		}
		if b.inner == nil {
			if len(b.cfg.Fallback) > 0 {
				return nil, errors.New("self-referencing provider DNS with fallback is unsupported")
			}
			for _, ns := range b.cfg.NameServer {
				if hfgjSelfDNS(ns, b.cfg.Listen) {
					return nil, errors.New("recursive provider DNS upstream")
				}
			}
			var policy []dns.Policy
			err := eachHFGJPolicy(b.cfg.Policy, func(domain string, list []string) error {
				for _, ns := range list {
					if hfgjSelfDNS(ns, b.cfg.Listen) {
						return errors.New("recursive provider DNS policy")
					}
				}
				nss, err := b.parse(list, b.cfg.RespectRules)
				if err != nil {
					return err
				}
				matcher, err := hfgjPolicyMatcher(domain)
				if err != nil {
					return err
				}
				policy = append(policy, dns.Policy{Domain: domain, Matcher: matcher, NameServers: nss})
				return nil
			})
			if err != nil {
				return nil, err
			}
			cfg, err := b.config(b.cfg.NameServer, policy, b.cfg.RespectRules)
			if err != nil {
				return nil, err
			}
			b.inner = dns.NewResolver(cfg).Resolver
			b.owner.owned = append(b.owner.owned, b.inner)
		}
		b.cache[key] = b.inner
		return b.inner, nil
	}
	cfg, err := b.config(servers, nil, false)
	if err != nil {
		return nil, err
	}
	r := dns.NewResolver(cfg).Resolver
	b.owner.owned = append(b.owner.owned, r)
	b.cache[key] = r
	return r, nil
}

func eachHFGJPolicy(node yaml.Node, fn func(string, []string) error) error {
	if node.Kind == 0 || node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return errors.New("invalid provider DNS policy")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		prefix := ""
		if strings.HasPrefix(key, "geosite:") {
			prefix = "geosite:"
			key = key[8:]
		} else if strings.HasPrefix(key, "rule-set:") {
			prefix = "rule-set:"
			key = key[9:]
		} else if strings.Contains(key, ":") {
			return errors.New("unsupported provider DNS policy")
		}
		var value any
		if err := node.Content[i+1].Decode(&value); err != nil {
			return errors.New("invalid provider DNS policy value")
		}
		servers, err := utils.ToStringSlice(value)
		if err != nil || len(servers) == 0 {
			return errors.New("empty or invalid provider DNS policy upstream")
		}
		for _, domain := range strings.Split(key, ",") {
			if prefix == "" {
				if _, err := trie.ValidAndSplitDomain(domain); err != nil {
					return errors.New("invalid provider DNS policy domain")
				}
			} else {
				if domain == "" {
					return errors.New("invalid provider DNS matcher")
				}
				domain = prefix + domain
			}
			if err := fn(domain, servers); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *hfgjProviderResolver) host(host string) (string, []netip.Addr, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		if ip, err := netip.ParseAddr(host); err == nil {
			return host, []netip.Addr{ip.Unmap()}, nil
		}
		if seen[host] {
			return "", nil, errors.New("cyclic provider alias")
		}
		seen[host] = true
		node := r.hosts.Search(host)
		if node == nil {
			return host, nil, nil
		}
		hv := node.Data()
		if !hv.IsDomain {
			return host, append([]netip.Addr(nil), hv.IPs...), nil
		}
		host = strings.ToLower(hv.Domain)
	}
	return "", nil, errors.New("provider alias chain is too long")
}

func (r *hfgjProviderResolver) lookup(ctx context.Context, host string, family int) ([]netip.Addr, error) {
	backend := r.selectBackend(host)
	target, ips, err := r.host(host)
	if err != nil {
		return nil, err
	}
	if ips != nil {
		result := make([]netip.Addr, 0, len(ips))
		for _, ip := range ips {
			if family == 0 || (family == 4 && ip.Is4()) || (family == 6 && ip.Is6()) {
				result = append(result, ip)
			}
		}
		if len(result) == 0 {
			return nil, resolver.ErrIPVersion
		}
		return result, nil
	}
	// Alias-only providers inherit the current main node resolver at query time.
	if backend == nil {
		switch family {
		case 4:
			return resolver.LookupIPv4WithResolver(ctx, target, resolver.ProxyServerHostResolver)
		case 6:
			return resolver.LookupIPv6WithResolver(ctx, target, resolver.ProxyServerHostResolver)
		default:
			return resolver.LookupIPWithResolver(ctx, target, resolver.ProxyServerHostResolver)
		}
	}
	switch family {
	case 4:
		return backend.LookupIPv4(ctx, target)
	case 6:
		return backend.LookupIPv6(ctx, target)
	default:
		return backend.LookupIP(ctx, target)
	}
}
func (r *hfgjProviderResolver) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.lookup(ctx, host, 0)
}
func (r *hfgjProviderResolver) LookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.lookup(ctx, host, 4)
}
func (r *hfgjProviderResolver) LookupIPv6(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.lookup(ctx, host, 6)
}
func (r *hfgjProviderResolver) IsScopedResolver() bool { return true }
func (r *hfgjProviderResolver) Invalid() bool          { return true } // upstream interface means usable
func (r *hfgjProviderResolver) ClearCache() {
	for _, backend := range r.owned {
		backend.ClearCache()
	}
}
func (r *hfgjProviderResolver) ResetConnection() {
	for _, backend := range r.owned {
		backend.ResetConnection()
	}
}
func (r *hfgjProviderResolver) ResolveECH(ctx context.Context, host string) ([]byte, error) {
	backend := r.selectBackend(host)
	target, _, err := r.host(host)
	if err != nil {
		return nil, err
	}
	if backend == nil {
		return resolver.ResolveECHWithResolver(ctx, target, resolver.ProxyServerHostResolver)
	}
	return backend.ResolveECH(ctx, target)
}
func (r *hfgjProviderResolver) ExchangeContext(ctx context.Context, m *D.Msg) (*D.Msg, error) {
	// This object is a node resolver, not a DNS listener/service.
	return nil, errors.New("provider node resolver does not serve DNS messages")
}

// Keep the same order as dns.NewResolver: consecutive domain entries share a
// trie (most-specific wins), while GeoSite/rule-set entries split those groups.
type hfgjDNSRoute struct {
	hosts   *trie.DomainTrie[resolver.Resolver]
	matcher C.DomainMatcher
	backend resolver.Resolver
}

func hfgjPolicyMatcher(domain string) (C.DomainMatcher, error) {
	if !strings.Contains(domain, ":") {
		return nil, nil
	}
	if dns.ProviderDomainMatcher == nil {
		return nil, errors.New("provider DNS matcher is not initialized")
	}
	matcher, err := dns.ProviderDomainMatcher(domain)
	if err != nil {
		return nil, errors.New("provider DNS matcher data unavailable")
	}
	return matcher, nil
}
func (r *hfgjProviderResolver) addRoute(domain string, backend resolver.Resolver) error {
	matcher, err := hfgjPolicyMatcher(domain)
	if err != nil {
		return err
	}
	if matcher != nil {
		r.routes = append(r.routes, hfgjDNSRoute{matcher: matcher, backend: backend})
		return nil
	}
	n := len(r.routes)
	if n == 0 || r.routes[n-1].hosts == nil {
		r.routes = append(r.routes, hfgjDNSRoute{hosts: trie.New[resolver.Resolver]()})
	}
	return r.routes[len(r.routes)-1].hosts.Insert(domain, backend)
}
func (r *hfgjProviderResolver) selectBackend(host string) resolver.Resolver {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, route := range r.routes {
		if route.hosts != nil {
			if node := route.hosts.Search(host); node != nil {
				return node.Data()
			}
		} else if route.matcher.MatchDomain(host) {
			return route.backend
		}
	}
	return r.backend
}
