package config

import (
	"errors"
	"strings"

	"github.com/metacubex/mihomo/component/geodata"
	"github.com/metacubex/mihomo/component/geodata/router"
	C "github.com/metacubex/mihomo/constant"
	T "github.com/metacubex/mihomo/tunnel"
)

type hfgjProviderGeoMatcher struct{ router.DomainMatcher }

func (m hfgjProviderGeoMatcher) MatchDomain(domain string) bool { return m.ApplyDomain(domain) }

func newHFGJProviderDomainMatcher(domain string) (C.DomainMatcher, error) {
	switch {
	case strings.HasPrefix(domain, "geosite:"):
		// Read/share existing main-config data. Provider parsing must not initialize
		// a listener, download geodata, or import an airport's rules/providers.
		matcher, err := geodata.LoadGeoSiteMatcher(domain[8:])
		if err != nil {
			return nil, err
		}
		return hfgjProviderGeoMatcher{matcher}, nil
	case strings.HasPrefix(domain, "rule-set:"):
		return parseDomainRuleSet(domain[9:], "provider-dns", T.RuleProviders())
	default:
		return nil, errors.New("unsupported provider DNS matcher")
	}
}
