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

func TestHTTPRouteCredentialScopeAndPluginInitialization(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(t.TempDir(), "account-a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := readyFixture(t)
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p Package
	json.Unmarshal(raw, &p)
	if !containsString(p.Permissions, "network") {
		p.Permissions = append(p.Permissions, "network")
	}
	p.HTTPRoutes = []HTTPRoute{{ID: "source-read", Endpoint: "https://provider.example/api/read", Capabilities: []string{"speech.synthesize"}, Credential: "session", CredentialHeader: "Cookie"}, {ID: "public-read", Endpoint: "https://provider.example/api/public", RefererPathPrefix: "/preview/", Capabilities: []string{"speech.synthesize"}}}
	schema := []byte(`{"type":"object","properties":{"session":{"type":"string","x-secret":true,"x-host-only":true},"ordinary_key":{"type":"string","x-secret":true}},"additionalProperties":false}`)
	if err = os.WriteFile(filepath.Join(dir, "config.schema.json"), schema, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(schema)
	p.Files["config.schema.json"] = hex.EncodeToString(sum[:])
	raw, _ = json.Marshal(p)
	os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600)
	prepared, err := s.Prepare(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Install(ctx, prepared.Token, prepared.Digest, p.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveConfiguration(ctx, p.ID, ConfigUpdate{Revision: 0, Values: map[string]json.RawMessage{}, Secrets: map[string]string{"session": "synthetic=private", "ordinary_key": "plugin-specific"}})
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.SetEnabled(ctx, p.ID, i.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := s.ConfigurationForPlugin(ctx, p.ID)
	if err != nil || len(plugin.Values["session"]) > 0 || string(plugin.Values["ordinary_key"]) != `"plugin-specific"` {
		t.Fatal("initialization secret policy", plugin, err)
	}
	j := Job{Plugin: p.ID, Version: p.Version, PackageDigest: i.Digest, ConfigRevision: "1", Tool: "speech.synthesize", Context: Invocation{Account: "account-a"}}
	cap := Capability{ID: j.Tool, Permissions: []string{"network"}}
	req, err := s.ResolveHTTPRequest(ctx, j, cap, "source-read", []byte(`{"id":"synthetic"}`))
	if err != nil || req.Headers["Cookie"] != "synthetic=private" || req.Endpoint != "https://provider.example/api/read" {
		t.Fatal("route preparation", err)
	}
	public, err := s.ResolveHTTPRequest(ctx, j, cap, "public-read", []byte(`{}`), "https://provider.example/preview/item?token=synthetic")
	if err != nil || public.Headers["Cookie"] != "" || public.Headers["Referer"] != "https://provider.example/preview/item?token=synthetic" {
		t.Fatal("credential leaked to uncredentialed route", err)
	}
	if _, err = s.ResolveHTTPRequest(ctx, j, cap, "source-read", []byte(`{}`), "https://evil.example/"); err == nil {
		t.Fatal("undeclared dynamic referer accepted")
	}
	for _, ref := range []string{"https://other.example/preview/item", "https://provider.example/other/item", "https://user@provider.example/preview/item", "https://provider.example/preview/%69tem"} {
		if _, err = s.ResolveHTTPRequest(ctx, j, cap, "public-read", []byte(`{}`), ref); err == nil {
			t.Fatal("invalid dynamic referer accepted")
		}
	}
	for _, alter := range []func(*Job){func(j *Job) { j.Context.Account = "account-b" }, func(j *Job) { j.ConfigRevision = "0" }, func(j *Job) { j.PackageDigest = "other" }, func(j *Job) { j.Version = "other" }, func(j *Job) { j.Tool = "other" }} {
		copy := j
		alter(&copy)
		if _, err = s.ResolveHTTPRequest(ctx, copy, cap, "source-read", []byte(`{}`)); err == nil {
			t.Fatal("changed authority accepted")
		}
	}
	for _, route := range []string{"other", "https://other.example/steal"} {
		if _, err = s.ResolveHTTPRequest(ctx, j, cap, route, []byte(`{}`)); err == nil {
			t.Fatal("undeclared route accepted")
		}
	}
	if _, err = s.ResolveHTTPRequest(ctx, j, Capability{ID: j.Tool}, "source-read", []byte(`{}`)); err == nil {
		t.Fatal("missing network capability accepted")
	}
	i, err = s.SetEnabled(ctx, p.ID, i.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveHTTPRequest(ctx, j, cap, "source-read", []byte(`{}`)); err == nil {
		t.Fatal("disabled plugin retained route")
	}
}

func TestHTTPRouteDeclarationRejectsCredentialDestinationsAndHeaders(t *testing.T) {
	valid := HTTPRoute{ID: "read", Endpoint: "https://provider.example/api/read", Capabilities: []string{"share.read"}, Credential: "session", CredentialHeader: "Cookie"}
	for _, alter := range []func(*HTTPRoute){
		func(r *HTTPRoute) { r.Endpoint = "http://provider.example/api/read" }, func(r *HTTPRoute) { r.Endpoint = "https://user@provider.example/api/read" }, func(r *HTTPRoute) { r.Endpoint = "https://provider.example/api?override=1" }, func(r *HTTPRoute) { r.CredentialHeader = "Host" }, func(r *HTTPRoute) { r.Headers = map[string]string{"Cookie": "untracked-secret"} }, func(r *HTTPRoute) { r.Headers = map[string]string{"Referer": "https://other.example/"} }, func(r *HTTPRoute) { r.Headers = map[string]string{"Accept": "a\r\nb"} },
	} {
		r := valid
		alter(&r)
		if validateHTTPRoutes(Package{Permissions: []string{"network"}, HTTPRoutes: []HTTPRoute{r}}) == nil {
			t.Fatal("invalid route accepted")
		}
	}
	if validateHTTPRoutes(Package{HTTPRoutes: []HTTPRoute{valid}}) == nil {
		t.Fatal("route lacks network permission")
	}
}
