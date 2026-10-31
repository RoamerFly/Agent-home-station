package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestModelRouteConfiguredDestinationAndPermissionIsolation(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := readyFixture(t)
	raw, _ := os.ReadFile(filepath.Join(dir, "plugin.json"))
	var p Package
	json.Unmarshal(raw, &p)
	p.Permissions = []string{"model.invoke"}
	p.HTTPRoutes = []HTTPRoute{{ID: "vision", Permission: "model.invoke", EndpointConfig: "vision_endpoint", Capabilities: []string{"speech.synthesize"}, Credential: "key", CredentialHeader: "Authorization", CredentialPrefix: "Bearer "}}
	files := map[string]string{
		"config.schema.json": `{"type":"object","properties":{"vision_endpoint":{"type":"string"},"key":{"type":"string","x-secret":true,"x-host-only":true}},"required":["vision_endpoint","key"],"additionalProperties":false}`,
		"capabilities.json":  `{"version":1,"capabilities":[{"id":"speech.synthesize","implementation":"ready","description":"synthetic","permissions":["model.invoke"],"input_schema":{"type":"object"},"output_schema":{"type":"object"},"discoverable":true}]}`,
	}
	for name, content := range files {
		b := []byte(content)
		os.WriteFile(filepath.Join(dir, name), b, 0600)
		sum := sha256.Sum256(b)
		p.Files[name] = hex.EncodeToString(sum[:])
	}
	raw, _ = json.Marshal(p)
	os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600)
	prepared, err := store.Prepare(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	i, err := store.Install(ctx, prepared.Token, prepared.Digest, p.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://provider.example/api", "https://provider.example/api?token=private", "https://provider.example/api#fragment", "file:///private"} {
		value, _ := json.Marshal(endpoint)
		if _, err := store.SaveConfiguration(ctx, p.ID, ConfigUpdate{Values: map[string]json.RawMessage{"vision_endpoint": value}, Secrets: map[string]string{"key": "synthetic-key"}}); err == nil {
			t.Fatal("unsafe configured endpoint accepted")
		}
	}
	_, err = store.SaveConfiguration(ctx, p.ID, ConfigUpdate{Values: map[string]json.RawMessage{"vision_endpoint": json.RawMessage(`"https://provider.example/chat/completions"`)}, Secrets: map[string]string{"key": "synthetic-key"}})
	if err != nil {
		t.Fatal(err)
	}
	i, err = store.SetEnabled(ctx, p.ID, i.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := store.ConfigurationForPlugin(ctx, p.ID)
	if err != nil || len(cfg.Values) != 1 || len(cfg.Values["key"]) != 0 {
		t.Fatal("host-held key leaked", err)
	}
	j := Job{Plugin: p.ID, Version: p.Version, PackageDigest: i.Digest, ConfigRevision: "1", Tool: "speech.synthesize", Context: Invocation{Account: "a"}}
	cap := Capability{ID: j.Tool, Permissions: []string{"model.invoke"}}
	req, err := store.ResolveModelRequest(ctx, j, cap, "vision", json.RawMessage(`{"model":"synthetic"}`))
	if err != nil || req.Endpoint != "https://provider.example/chat/completions" || req.Headers["Authorization"] != "Bearer synthetic-key" {
		t.Fatal("configured model route failed", err)
	}
	if _, err = store.ResolveHTTPRequest(ctx, j, Capability{ID: j.Tool, Permissions: []string{"network", "model.invoke"}}, "vision", json.RawMessage(`{}`)); err == nil {
		t.Fatal("network method invoked model route")
	}
	if _, err = store.ResolveModelRequest(ctx, j, Capability{ID: j.Tool, Permissions: []string{"network"}}, "vision", json.RawMessage(`{}`)); err == nil {
		t.Fatal("network permission invoked model route")
	}
	j.ConfigRevision = "0"
	if _, err = store.ResolveModelRequest(ctx, j, cap, "vision", json.RawMessage(`{}`)); err == nil {
		t.Fatal("stale configuration accepted")
	}
}

func TestModelRouteDeclarationRequiresOneBoundDestination(t *testing.T) {
	base := HTTPRoute{ID: "vision", Permission: "model.invoke", EndpointConfig: "vision_endpoint", Capabilities: []string{"media.observe"}, Credential: "key", CredentialHeader: "Authorization", CredentialPrefix: "Bearer "}
	for _, change := range []func(*HTTPRoute){
		func(r *HTTPRoute) { r.Endpoint = "https://other.example/api" },
		func(r *HTTPRoute) { r.EndpointConfig = "../private" },
		func(r *HTTPRoute) { r.Headers = map[string]string{"Referer": "https://other.example/"} },
		func(r *HTTPRoute) { r.CredentialPrefix = "Basic " },
		func(r *HTTPRoute) { r.Permission = "workspace.read" },
	} {
		r := base
		change(&r)
		if validateHTTPRoutes(Package{Permissions: []string{"model.invoke"}, HTTPRoutes: []HTTPRoute{r}}) == nil {
			t.Fatal("invalid model route declaration accepted")
		}
	}
}
