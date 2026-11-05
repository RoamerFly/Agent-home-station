package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// HTTPRoute is part of the approved package digest. Plugins select a route ID,
// never an arbitrary destination/header for a host-held credential.
type HTTPRoute struct {
	ID                string            `json:"id"`
	Endpoint          string            `json:"endpoint"`
	EndpointConfig    string            `json:"endpoint_config,omitempty"`
	Optional          bool              `json:"optional,omitempty"`
	Permission        string            `json:"permission,omitempty"`
	Capabilities      []string          `json:"capabilities"`
	Credential        string            `json:"credential,omitempty"`
	CredentialHeader  string            `json:"credential_header,omitempty"`
	CredentialPrefix  string            `json:"credential_prefix,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	RefererPathPrefix string            `json:"referer_path_prefix,omitempty"`
}

var routeConfigName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// HostHTTPRequest must never be serialized into a tool result or diagnostic.
type HostHTTPRequest struct {
	Endpoint string
	Headers  map[string]string
	Body     json.RawMessage
}

func (r HTTPRoute) permission() string {
	if r.Permission == "" {
		return "network"
	}
	return r.Permission
}

func validRouteEndpoint(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.ContainsAny(raw, "\\\r\n\x00") {
		return nil, errors.New("HTTP route requires exact HTTPS endpoint")
	}
	return u, nil
}

func validateHTTPRoutes(p Package) error {
	seen := map[string]bool{}
	for _, r := range p.HTTPRoutes {
		if !identifier.MatchString(r.ID) || seen[r.ID] || (r.permission() != "network" && r.permission() != "model.invoke") || !containsString(p.Permissions, r.permission()) {
			return errors.New("invalid HTTP route declaration")
		}
		seen[r.ID] = true
		if r.Optional && r.EndpointConfig == "" {
			return errors.New("optional route requires configurable endpoint")
		}
		var u *url.URL
		if r.EndpointConfig != "" {
			if r.Endpoint != "" || !routeConfigName.MatchString(r.EndpointConfig) || r.RefererPathPrefix != "" {
				return errors.New("invalid configurable route endpoint")
			}
		} else {
			var e error
			u, e = validRouteEndpoint(r.Endpoint)
			if e != nil {
				return e
			}
		}
		if len(r.Capabilities) == 0 {
			return errors.New("HTTP route capability missing")
		}
		if r.RefererPathPrefix != "" && (!strings.HasPrefix(r.RefererPathPrefix, "/") || strings.ContainsAny(r.RefererPathPrefix, "?%#\\\r\n") || strings.Contains(r.RefererPathPrefix, "..")) {
			return errors.New("invalid route referer path prefix")
		}
		for _, c := range r.Capabilities {
			if !capabilityID.MatchString(c) {
				return errors.New("invalid HTTP route capability")
			}
		}
		if (r.Credential == "") != (r.CredentialHeader == "") || r.Credential != "" && r.CredentialHeader != "Cookie" && r.CredentialHeader != "Authorization" {
			return errors.New("invalid HTTP route credential")
		}
		if r.CredentialPrefix != "" && (r.CredentialPrefix != "Bearer " || r.Credential == "" || r.CredentialHeader != "Authorization") {
			return errors.New("invalid HTTP credential prefix")
		}
		for k, v := range r.Headers {
			if k != "Accept" && k != "User-Agent" && k != "Origin" && k != "Referer" || strings.ContainsAny(v, "\r\n\x00") {
				return errors.New("invalid fixed HTTP header")
			}
			if k == "Origin" || k == "Referer" {
				h, e := url.Parse(v)
				if e != nil || u == nil || h.Scheme != u.Scheme || h.Host != u.Host || h.User != nil || h.Fragment != "" {
					return errors.New("HTTP route referer must share destination origin")
				}
			}
		}
	}
	return nil
}
func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func validateConfiguredRoutes(p Package, values map[string]json.RawMessage) error {
	for _, r := range p.HTTPRoutes {
		if r.EndpointConfig == "" {
			continue
		}
		var endpoint string
		if _, present := values[r.EndpointConfig]; !present && r.Optional {
			continue
		}
		if json.Unmarshal(values[r.EndpointConfig], &endpoint) != nil {
			return errors.New("route endpoint configuration missing")
		}
		if _, err := validRouteEndpoint(endpoint); err != nil {
			return err
		}
	}
	return nil
}

func validateHTTPRouteResources(dir string, p Package) error {
	if len(p.HTTPRoutes) == 0 {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.schema.json"))
	if err != nil {
		return err
	}
	var schema ConfigSchema
	if json.Unmarshal(raw, &schema) != nil {
		return errors.New("invalid HTTP route credential schema")
	}
	raw, err = os.ReadFile(filepath.Join(dir, "capabilities.json"))
	if err != nil {
		return err
	}
	var doc struct {
		Capabilities []Capability `json:"capabilities"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return errors.New("invalid HTTP route capabilities")
	}
	capabilities := map[string][]string{}
	for _, c := range doc.Capabilities {
		capabilities[c.ID] = c.Permissions
	}
	for _, r := range p.HTTPRoutes {
		if r.EndpointConfig != "" {
			field, ok := schema.Properties[r.EndpointConfig]
			if !ok || field.Secret || field.Type != "string" || !r.Optional && !containsString(schema.Required, r.EndpointConfig) {
				return errors.New("route endpoint requires declared required string configuration")
			}
		}
		if r.Credential != "" {
			field, ok := schema.Properties[r.Credential]
			if !ok || !field.Secret || !field.HostOnly || field.Type != "string" || len(field.Default) > 0 {
				return errors.New("HTTP route requires declared host-held secret")
			}
		}
		for _, c := range r.Capabilities {
			if !containsString(capabilities[c], r.permission()) {
				return errors.New("HTTP route references missing permitted capability")
			}
		}
	}
	return nil
}

// ResolveHTTPRequest performs account/version/config/grant checks immediately
// before transport. The caller must additionally bind provider payload fields to
// the original authorized source; network grants alone do not prove that binding.
func (s *Store) ResolveHTTPRequest(ctx context.Context, j Job, cap Capability, route string, body json.RawMessage, referer ...string) (HostHTTPRequest, error) {
	return s.resolveRouteRequest(ctx, j, cap, route, body, "network", referer...)
}

func (s *Store) ResolveModelRequest(ctx context.Context, j Job, cap Capability, route string, body json.RawMessage) (HostHTTPRequest, error) {
	return s.resolveRouteRequest(ctx, j, cap, route, body, "model.invoke")
}

func (s *Store) resolveRouteRequest(ctx context.Context, j Job, cap Capability, route string, body json.RawMessage, permission string, referer ...string) (HostHTTPRequest, error) {
	var result HostHTTPRequest
	i, err := s.Installed(ctx, j.Plugin)
	if err != nil || !i.Enabled || i.Removed || j.Context.Account != s.account || j.Version != i.Package.Version || j.PackageDigest != i.Digest || cap.ID != j.Tool || !containsString(cap.Permissions, permission) || !containsString(i.Permissions, permission) {
		return result, errors.New("HTTP route authorization changed")
	}
	if err = validateHTTPRoutes(i.Package); err != nil {
		return result, err
	}
	var selected *HTTPRoute
	for _, r := range i.Package.HTTPRoutes {
		if r.ID == route && r.permission() == permission && containsString(r.Capabilities, cap.ID) {
			copy := r
			selected = &copy
			break
		}
	}
	if selected == nil {
		return result, errors.New("HTTP route unavailable for capability")
	}
	var object map[string]json.RawMessage
	limit := 1 << 20
	if permission == "model.invoke" {
		limit = 32 << 20
	}
	if len(body) > limit || json.Unmarshal(body, &object) != nil || object == nil {
		return result, errors.New("HTTP route body exceeds transport limit or is not an object")
	}
	config, err := s.ConfigurationForExecution(ctx, j.Plugin)
	if err != nil || strconv.Itoa(config.Revision) != j.ConfigRevision {
		return result, errors.New("HTTP route configuration changed")
	}
	endpoint := selected.Endpoint
	if selected.EndpointConfig != "" {
		if json.Unmarshal(config.Values[selected.EndpointConfig], &endpoint) != nil {
			return result, errors.New("route endpoint configuration missing")
		}
		if _, err := validRouteEndpoint(endpoint); err != nil {
			return result, err
		}
	}
	result = HostHTTPRequest{Endpoint: endpoint, Headers: map[string]string{}, Body: append(json.RawMessage(nil), body...)}
	for k, v := range selected.Headers {
		result.Headers[k] = v
	}
	if len(referer) > 1 {
		return HostHTTPRequest{}, errors.New("invalid route referer")
	}
	reference := ""
	if len(referer) == 1 {
		reference = referer[0]
	}
	if reference != "" || selected.RefererPathPrefix != "" {
		u, e := url.Parse(reference)
		endpoint, _ := url.Parse(selected.Endpoint)
		if e != nil || selected.RefererPathPrefix == "" || len(reference) > 16384 || u.Scheme != endpoint.Scheme || u.Host != endpoint.Host || u.User != nil || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, selected.RefererPathPrefix) || strings.ContainsAny(reference, "\\\r\n\x00") {
			return HostHTTPRequest{}, errors.New("route referer outside approved origin/path")
		}
		result.Headers["Referer"] = reference
	}
	if selected.Credential != "" {
		schema, err := s.ConfigSchema(ctx, j.Plugin)
		if err != nil {
			return HostHTTPRequest{}, err
		}
		field, ok := schema.Properties[selected.Credential]
		if !ok || !field.Secret || !field.HostOnly {
			return HostHTTPRequest{}, errors.New("HTTP route credential must be host-held secret")
		}
		var secret string
		if json.Unmarshal(config.Values[selected.Credential], &secret) != nil || secret == "" || len(secret) > 16384 || strings.ContainsAny(secret, "\r\n\x00") {
			return HostHTTPRequest{}, errors.New("HTTP route credential unavailable")
		}
		result.Headers[selected.CredentialHeader] = selected.CredentialPrefix + secret
	}
	return result, nil
}
