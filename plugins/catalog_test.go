package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestContractForWebSearchPreservesExactManifestDigestAndDeclarations(t *testing.T) {
	contract, err := ContractFor("web-search")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := declarations.ReadFile("web-search/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if contract.ManifestSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("manifest digest = %q, want exact bytes digest", contract.ManifestSHA256)
	}
	backend, backendOK := contract.Configuration.Properties["search_backend"]
	key, keyOK := contract.Configuration.Properties["search_key"]
	if !backendOK || backend.Type != "string" || !keyOK || !key.Secret || !key.HostOnly {
		t.Fatalf("web search configuration declaration missing: %#v", contract.Configuration.Properties)
	}
	if len(contract.UI) == 0 || len(contract.Capabilities) == 0 {
		t.Fatal("declarative UI or capabilities were not embedded")
	}
}

func TestContractForRejectsUnsafeOrUnknownID(t *testing.T) {
	for _, id := range []string{"", "../web-search", "not-installed"} {
		if _, err := ContractFor(id); err == nil {
			t.Fatal("expected unavailable contract for", id)
		}
	}
}
