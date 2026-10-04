package provider_test

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/component/geodata"
	"github.com/metacubex/mihomo/component/geodata/router"
	"github.com/metacubex/mihomo/component/resolver"
	_ "github.com/metacubex/mihomo/config" // registers the real upstream DNS URI parser
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	D "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type localDNS struct {
	addr    string
	mu      sync.Mutex
	queries []string
}

func serveDNS(t *testing.T) *localDNS {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &localDNS{addr: pc.LocalAddr().String()}
	server := &D.Server{PacketConn: pc, Handler: D.HandlerFunc(func(w D.ResponseWriter, m *D.Msg) {
		reply := new(D.Msg)
		reply.SetReply(m)
		for _, q := range m.Question {
			s.mu.Lock()
			s.queries = append(s.queries, q.Name)
			s.mu.Unlock()
			if q.Qtype == D.TypeA {
				reply.Answer = append(reply.Answer, &D.A{Hdr: D.RR_Header{Name: q.Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 60}, A: net.ParseIP("127.0.0.1").To4()})
			}
		}
		_ = w.WriteMsg(reply)
	})}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown(); _ = pc.Close() })
	return s
}
func (s *localDNS) saw(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.queries {
		if q == name+"." {
			return true
		}
	}
	return false
}

func serveTCP(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(io.Discard, conn) }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}
func metadata(network C.NetWork) *C.Metadata {
	return &C.Metadata{NetWork: network, DstIP: netip.MustParseAddr("192.0.2.10"), DstPort: 443}
}
func ssPayload(port int, alias, upstream string) []byte {
	return []byte(fmt.Sprintf("proxies:\n  - {name: node, type: ss, server: a.example, port: %d, cipher: aes-128-gcm, password: test-only, udp: true}\nhosts:\n  a.example: %s\ndns:\n  enable: true\n  use-hosts: true\n  listen: 127.0.0.1:7874\n  proxy-server-nameserver: [udp://127.0.0.1:7874]\n  nameserver: [udp://%s]\n  default-nameserver: [system]\n", port, alias, upstream))
}
func withHome(t *testing.T) string {
	t.Helper()
	old := C.Path.HomeDir()
	home := t.TempDir()
	C.SetHomeDir(home)
	t.Cleanup(func() { C.SetHomeDir(old) })
	return home
}
func fileProvider(t *testing.T, name string, body []byte) P.ProxyProvider {
	t.Helper()
	path := filepath.Join(C.Path.HomeDir(), name+".yaml")
	require.NoError(t, os.WriteFile(path, body, 0600))
	p, err := provider.ParseProxyProvider(name, map[string]any{"type": "file", "path": path}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.(io.Closer).Close() })
	require.NoError(t, p.Initial())
	return p
}
func dialSS(t *testing.T, node C.Proxy) C.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := node.DialContext(ctx, metadata(C.TCP))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestHFGJProviderDNSIsolationAndPolicy(t *testing.T) {
	withHome(t)
	dns1, dns2 := serveDNS(t), serveDNS(t)
	port := serveTCP(t)
	globals, globalNode := resolver.DefaultResolver, resolver.ProxyServerHostResolver
	p1 := fileProvider(t, "one", ssPayload(port, "b1.example", dns1.addr))
	p2 := fileProvider(t, "two", ssPayload(port, "b2.example", dns2.addr))
	dialSS(t, p1.Proxies()[0])
	dialSS(t, p2.Proxies()[0])
	require.True(t, dns1.saw("b1.example"))
	require.False(t, dns1.saw("b2.example"))
	require.True(t, dns2.saw("b2.example"))
	require.False(t, dns2.saw("b1.example"))
	require.True(t, globals == resolver.DefaultResolver)
	require.True(t, globalNode == resolver.ProxyServerHostResolver)
	// Outer node policy matches A; inner self-DNS policy matches mapped B.
	body := string(ssPayload(port, "policy.example", dns1.addr)) + fmt.Sprintf("  nameserver-policy:\n    'policy.example': [udp://%s]\n", dns2.addr)
	p3 := fileProvider(t, "inner-policy", []byte(body))
	dialSS(t, p3.Proxies()[0])
	require.True(t, dns2.saw("policy.example"))
	require.False(t, dns1.saw("policy.example"))
	body = string(ssPayload(port, "outer.example", dns1.addr)) + fmt.Sprintf("  proxy-server-nameserver-policy:\n    'a.example': [udp://%s]\n", dns2.addr)
	p4 := fileProvider(t, "outer-policy", []byte(body))
	dialSS(t, p4.Proxies()[0])
	require.True(t, dns2.saw("outer.example"))
	require.False(t, dns1.saw("outer.example"))
}

func TestHFGJProviderShadowsocksUDP(t *testing.T) {
	withHome(t)
	dns := serveDNS(t)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer pc.Close()
	p := fileProvider(t, "udp", ssPayload(pc.LocalAddr().(*net.UDPAddr).Port, "udp.example", dns.addr))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := p.Proxies()[0].ListenPacketContext(ctx, metadata(C.UDP))
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.WriteTo([]byte("mock packet"), &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443})
	require.NoError(t, err)
	require.NoError(t, pc.SetReadDeadline(time.Now().Add(3*time.Second)))
	buf := make([]byte, 2048)
	n, _, err := pc.ReadFrom(buf)
	require.NoError(t, err)
	require.Positive(t, n)
	require.True(t, dns.saw("udp.example"))
}

func TestHFGJProviderTLSKeepsSNI(t *testing.T) {
	withHome(t)
	dns := serveDNS(t)
	for _, protocol := range []string{"anytls", "trojan"} {
		t.Run(protocol, func(t *testing.T) {
			seen := make(chan string, 4)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) { seen <- hello.ServerName; return nil, nil }}
			server.StartTLS()
			defer server.Close()
			port := server.Listener.Addr().(*net.TCPAddr).Port
			body := fmt.Sprintf("proxies:\n  - {name: node, type: %s, server: a.example, port: %d, password: test-only, sni: tls.example, skip-cert-verify: true, udp: true, tfo: true}\nhosts: {a.example: tls-target.example}\ndns:\n  proxy-server-nameserver: [udp://%s]\n  default-nameserver: [system]\n", protocol, port, dns.addr)
			p := fileProvider(t, protocol, []byte(body))
			require.True(t, p.Proxies()[0].ProxyInfo().TFO)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, _ := p.Proxies()[0].DialContext(ctx, metadata(C.TCP))
			if conn != nil {
				_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
				_, _ = conn.Write([]byte("trigger lazy handshake"))
				_ = conn.Close()
			}
			select {
			case name := <-seen:
				require.Equal(t, "tls.example", name)
			case <-ctx.Done():
				t.Fatal("TLS handshake did not reach mock server")
			}
			require.True(t, dns.saw("tls-target.example"))
		})
	}
}

func TestHFGJProviderUpdatesAndCache(t *testing.T) {
	home := withHome(t)
	dns1, dns2 := serveDNS(t), serveDNS(t)
	port := serveTCP(t)
	var mu sync.Mutex
	body := ssPayload(port, "old.example", dns1.addr)
	var reads atomic.Int32
	var failDownload atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if failDownload.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(body)
	}))
	defer server.Close()
	setBody := func(b []byte) { mu.Lock(); body = b; mu.Unlock() }
	path := filepath.Join(home, "http.yaml")
	mapping := map[string]any{"type": "http", "url": server.URL, "path": path, "interval": 3600}
	p, err := provider.ParseProxyProvider("http", mapping, nil)
	require.NoError(t, err)
	defer p.(io.Closer).Close()
	require.NoError(t, p.Initial())
	require.EqualValues(t, 1, reads.Load())
	old := p.Proxies()[0]
	live := dialSS(t, old)
	require.True(t, dns1.saw("old.example"))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, body, before)
	require.NoError(t, p.Update())
	require.EqualValues(t, 2, reads.Load())
	require.Same(t, old, p.Proxies()[0])
	failDownload.Store(true)
	require.Error(t, p.Update())
	require.Same(t, old, p.Proxies()[0])
	afterDownload, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, afterDownload)
	failDownload.Store(false)
	// Only hosts changes. A new resolver and node generation must be published.
	newBody := ssPayload(port, "new.example", dns1.addr)
	setBody(newBody)
	require.NoError(t, p.Update())
	require.EqualValues(t, 4, reads.Load())
	require.NotSame(t, old, p.Proxies()[0])
	dialSS(t, p.Proxies()[0])
	require.True(t, dns1.saw("new.example"))
	_, err = live.Write([]byte("old connection remains usable"))
	require.NoError(t, err)
	cached, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, newBody, cached)
	// Only DNS changes; it must replace the parser state as well.
	dnsBody := ssPayload(port, "new.example", dns2.addr)
	setBody(dnsBody)
	require.NoError(t, p.Update())
	dialSS(t, p.Proxies()[0])
	require.True(t, dns2.saw("new.example"))
	current := p.Proxies()[0]
	cached, err = os.ReadFile(path)
	require.NoError(t, err)
	setBody(ssPayload(port, "a.example", dns1.addr))
	require.Error(t, p.Update())
	require.Same(t, current, p.Proxies()[0])
	afterAlias, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, cached, afterAlias)
	setBody([]byte("proxies: [broken"))
	require.Error(t, p.Update())
	require.Same(t, current, p.Proxies()[0])
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, cached, after)
	// Cache write failure must not publish the already prepared candidate.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0700))
	setBody(ssPayload(port, "unpublished.example", dns1.addr))
	require.Error(t, p.Update())
	require.Same(t, current, p.Proxies()[0])
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, cached, 0600))
	// Restart uses raw cache, without downloading or rewriting it.
	count := reads.Load()
	stat, err := os.Stat(path)
	require.NoError(t, err)
	restart, err := provider.ParseProxyProvider("restart", mapping, nil)
	require.NoError(t, err)
	defer restart.(io.Closer).Close()
	require.NoError(t, restart.Initial())
	require.Equal(t, count, reads.Load())
	dialSS(t, restart.Proxies()[0])
	after, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, cached, after)
	later, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, stat.ModTime(), later.ModTime())
	// Concurrent manual requests publish one complete generation, while the
	// timer metadata reader can run without racing with writes/hash reads.
	setBody(cached)
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- p.Update() }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.Same(t, current, p.Proxies()[0])

}

func TestHFGJProviderTimerUpdate(t *testing.T) {
	home := withHome(t)
	dns := serveDNS(t)
	port := serveTCP(t)
	oldBody, newBody := ssPayload(port, "timer-old.example", dns.addr), ssPayload(port, "timer-new.example", dns.addr)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(newBody) }))
	defer server.Close()
	path := filepath.Join(home, "timer.yaml")
	require.NoError(t, os.WriteFile(path, oldBody, 0600))
	p, err := provider.ParseProxyProvider("timer", map[string]any{"type": "http", "url": server.URL, "path": path, "interval": 1}, nil)
	require.NoError(t, err)
	defer p.(io.Closer).Close()
	require.NoError(t, p.Initial())
	old := p.Proxies()[0]
	require.Eventually(t, func() bool { return p.Proxies()[0] != old }, 5*time.Second, 20*time.Millisecond)
	dialSS(t, p.Proxies()[0])
	require.True(t, dns.saw("timer-new.example"))
}

func TestHFGJProviderDoHAndBootstrap(t *testing.T) {
	withHome(t)
	dns := serveDNS(t)
	port := serveTCP(t)
	var queries atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		if err != nil {
			http.Error(w, "read", 400)
			return
		}
		m := new(D.Msg)
		if err = m.Unpack(body); err != nil {
			http.Error(w, "unpack", 400)
			return
		}
		queries.Add(1)
		reply := new(D.Msg)
		reply.SetReply(m)
		for _, q := range m.Question {
			if q.Qtype == D.TypeA {
				reply.Answer = append(reply.Answer, &D.A{Hdr: D.RR_Header{Name: q.Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 60}, A: net.ParseIP("127.0.0.1").To4()})
			}
		}
		packed, _ := reply.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(packed)
	}))
	defer server.Close()
	dohPort := server.Listener.Addr().(*net.TCPAddr).Port
	body := fmt.Sprintf("proxies:\n  - {name: node, type: ss, server: a.example, port: %d, cipher: aes-128-gcm, password: test-only}\nhosts: {a.example: doh-target.example}\ndns:\n  proxy-server-nameserver: [https://seed.example:%s/dns-query#skip-cert-verify=true]\n  default-nameserver: [udp://%s]\n", port, strconv.Itoa(dohPort), dns.addr)
	p := fileProvider(t, "doh", []byte(body))
	dialSS(t, p.Proxies()[0])
	require.Positive(t, queries.Load())
	require.True(t, dns.saw("seed.example"))
	require.False(t, dns.saw("doh-target.example"))
}

func TestHFGJProviderGeoSitePolicy(t *testing.T) {
	withHome(t)
	oldLoader := geodata.LoaderName()
	geodata.SetLoader("standard")
	geodata.ClearGeoSiteCache()
	t.Cleanup(func() { geodata.ClearGeoSiteCache(); geodata.SetLoader(oldLoader) })
	data, err := proto.Marshal(&router.GeoSiteList{Entry: []*router.GeoSite{{CountryCode: "HFGJ-FIXTURE", Domain: []*router.Domain{{Type: router.Domain_Full, Value: "geo.example"}, {Type: router.Domain_Full, Value: "a.example"}}}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(C.Path.GeoSite(), data, 0600))
	dns1, dns2 := serveDNS(t), serveDNS(t)
	port := serveTCP(t)
	body := string(ssPayload(port, "geo.example", dns1.addr)) + fmt.Sprintf("  nameserver-policy:\n    'geosite:hfgj-fixture': [udp://%s]\n", dns2.addr)
	p := fileProvider(t, "geosite-inner", []byte(body))
	dialSS(t, p.Proxies()[0])
	require.True(t, dns2.saw("geo.example"))
	require.False(t, dns1.saw("geo.example"))
	body = string(ssPayload(port, "outer-geo.example", dns1.addr)) + fmt.Sprintf("  proxy-server-nameserver-policy:\n    'geosite:hfgj-fixture': [udp://%s]\n    'a.example': [udp://%s]\n", dns2.addr, dns1.addr)
	p = fileProvider(t, "geosite-outer", []byte(body))
	dialSS(t, p.Proxies()[0])
	require.True(t, dns2.saw("outer-geo.example"))
	require.False(t, dns1.saw("outer-geo.example"))
}
