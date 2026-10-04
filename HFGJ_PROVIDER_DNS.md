# HFGJ provider hosts and node DNS

This patch belongs to the stable `hfgj` branch (currently based on upstream
`Meta` v1.19.31). `Meta` and `Alpha` remain upstream mirrors. Port the patch to
`hfgj-alpha` separately after stable validation; do not merge the stable branch
into the Alpha patch branch. The existing Windows TUN DNS patch is independent.

## Behavior

Full YAML subscriptions can contain `proxies`, top-level `hosts`, and `dns`.
The provider parser consumes the hosts and explicit node-DNS metadata from the
same response that supplied its nodes. Provider names, airport names, alias
endpoints and upstream addresses are not hardcoded. No second subscription
request or post-download cache rewrite is added.

Each parsed generation has its own hosts, resolver clients and DNS caches.
Nodes retain their original `server`, SNI and transport/socket settings. Only
resolution of the upstream node address uses this state; application DNS,
main-config hosts, routing rules and TUN configuration retain their behavior.

Changed HTTP responses follow the existing order:

```
read response into memory -> compare raw hash
  -> parse nodes + prepare provider resolver + bind new nodes
  -> write original response bytes -> publish new nodes
```

Cache startup uses the same parser. Unchanged hashes retain the existing nodes
and caches. Parse/write failures do not publish a candidate. DNS failures after
publication are connection failures, not automatic generation rollback. Active
connections are not deliberately disconnected by a provider update.

A new core needs one restart to install. Subsequent provider API/timer updates
need no main-config reload or process restart. Nikki, Zashboard and Verge use
their existing provider update interfaces.

## DNS selection

- With `use-hosts` absent/true, provider hosts precede main-config hosts for the
  provider's own node lookup. Alias chains and IP lists are supported. Cycles,
  malformed keys/targets and chains longer than 64 steps are rejected.
- Explicit `proxy-server-nameserver` and its policy select node upstreams. Outer
  node policy matches the original server name, before alias rewriting.
- Plain UDP/TCP self-reference is recognized only when the loopback endpoint
  matches this response's `dns.listen` address/family and port (including a
  matching wildcard listener). It expands in memory to the response's
  `nameserver` and `nameserver-policy`; inner policy matches the mapped name.
  No airport DNS listener is created, and no fake-IP pool is imported.
- Other DNS endpoints remain external; a loopback address alone does not mean
  self-reference. Encrypted or parameterized local endpoints are not expanded.
- URI parameters and H3/respect-rules options reuse the upstream parser and DNS
  clients. Endpoint bootstrap uses `default-nameserver` (IP endpoints/system);
  absent bootstrap settings use the system resolver.
- Alias-only subscriptions inherit the current main node resolver after mapping.
  Ordinary `nameserver` without explicit node DNS is not promoted to node DNS.
- `enable: false` disables subscription DNS selection; `use-hosts: false`
  disables subscription hosts. Main IPv6 restrictions and node IP preferences
  still apply. An explicitly supplied API dialer retains upstream precedence.
- Domain, GeoSite and main-config rule-set policy matchers retain policy order.
  GeoSite reads existing core data without downloading it. Rule-set references
  must already exist in the main core; airport rule providers are not imported.
  Missing matcher data rejects the candidate.

## Supported scope and explicit errors

Scoped node resolution supports AnyTLS, Trojan, Shadowsocks, SSR, SOCKS5, HTTP,
VMess, VLESS and Snell. Shared TCP dialing retains socket options; SS/SSR/SOCKS5
UDP endpoint resolution is also connected. `dialer-proxy` receives the resolved
node IP while TLS retains its original hostname. Current end-to-end mock tests
cover AnyTLS, Trojan, and Shadowsocks, including native SS UDP.

Other protocols with separate endpoint/QUIC resolvers are rejected when scoped
metadata is active; pure-node subscriptions retain upstream protocol support.
Self-reference mixed with external servers in the same node-DNS list, or
self-reference requiring fallback behavior, is currently rejected. These cases
need a separate explicit integration, not silent fallback to global settings.

Subscription-local caches expire normally and are replaced with a changed
provider generation. The existing global DNS cache API is not extended with a
registry of these caches.

Fetcher hash/timestamp access is synchronized with a separate metadata mutex.
The original publication lock and download/cache/callback order stay intact;
callbacks can read timestamps without acquiring their own publication lock.

## Verification and maintenance

Run the HFGJ tests and retain the existing TUN regression check:

```
go test -race ./adapter/provider ./adapter/outbound ./component/resolver ./component/resource ./config ./dns ./listener/sing_tun -count=1
```

An optional `HFGJ_PROVIDER_SAMPLE_DIR` allows static local parsing of private
samples. The test reports counts only and uses synthetic GeoSite data. Do not
commit subscriptions, credentials, private domains or raw cache files.

The historical Windows workflow now includes these source paths and the test
gate, keeping its MetaCubeX Go 1.26 toolchain and adding Linux ARM64 and macOS
targets. See `HFGJ_CORE_UPDATES.md` for update sources and package naming. Pushing
to the patch branches can publish rolling releases; local validation is not
permission to push or publish. OpenWrt package feeds and real-device tests remain
separate steps.

Local validation on 2026-10-04 used the official MetaCubeX Go 1.26 build asset
(actual version go1.26.8, darwin/arm64 host); its SHA256 matched the published
asset digest. The seven-package command above also passed with `-tags
with_gvisor`, including the existing TUN regression. CGO-disabled `with_gvisor`
builds passed for Windows amd64/v2, Windows arm64 and Linux arm64/v8.0. The Linux
ELF was verified to have neither an interpreter nor a dynamic segment. These
are local tests and cross-builds, not GitHub Actions or real-device execution.
That first validation preceded the core-update-source change. The workflow now
prepares five platform assets; its live execution, deployment and Alpha
integration remain separate decisions.

When updating upstream, inspect equivalent upstream capabilities first. Remove
obsolete HFGJ logic rather than carrying duplicate behavior. Check the provider
parser, DNS callbacks, dialing/UDP hooks and Fetcher lifecycle, then rerun tests
and both Windows builds before release.
