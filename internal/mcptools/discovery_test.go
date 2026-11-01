package mcptools

import "testing"

func TestDiscoverySourcesValidate(t *testing.T) {
	for _, source := range DefaultDiscoverySources() {
		if err := ValidateDiscoverySource(source); err != nil {
			t.Fatal(source, err)
		}
	}
	for _, modify := range []func(*DiscoverySource){
		func(source *DiscoverySource) { source.URL = "https://example.org/arbitrary-api" },
		func(source *DiscoverySource) { source.Kind = "arbitrary" },
		func(source *DiscoverySource) { source.Type = "arbitrary" },
		func(source *DiscoverySource) {
			source.URL = "https://user:pass@api.bilibili.com/x/web-interface/popular"
		},
	} {
		source := DefaultDiscoverySources()[0]
		modify(&source)
		if ValidateDiscoverySource(source) == nil {
			t.Fatal("invalid source accepted", source)
		}
	}
}
