package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HTTPRoute is part of the approved package digest. Plugins select a route ID,
// never an arbitrary destination/header for a host-held credential.
type HTTPRoute struct {
	ID                string            `json:"id"`
	Endpoint          string            `json:"endpoint"`
	Capabilities      []string          `json:"capabilities"`
	Credential        string            `json:"credential,omitempty"`
	CredentialHeader  string            `json:"credential_header,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	RefererPathPrefix string            `json:"referer_path_prefix,omitempty"`
}

// HostHTTPRequest must never be serialized into a tool result or diagnostic.
type HostHTTPRequest struct {
	Endpoint string
	Headers  map[string]string
	Body     json.RawMessage
}

func validateHTTPRoutes(p Package) error {
	seen := map[string]bool{}
	for _, r := range p.HTTPRoutes {
		if !identifier.MatchString(r.ID) || seen[r.ID] || !containsString(p.Permissions, "network") {
			return errors.New("invalid HTTP route declaration")
		}
		seen[r.ID] = true
		u, e := url.Parse(r.Endpoint)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.ContainsAny(r.Endpoint, "\\\r\n\x00") {
			return errors.New("HTTP route requires exact HTTPS endpoint")
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
		for k, v := range r.Headers {
			if k != "Accept" && k != "User-Agent" && k != "Origin" && k != "Referer" || strings.ContainsAny(v, "\r\n\x00") {
				return errors.New("invalid fixed HTTP header")
			}
			if k == "Origin" || k == "Referer" {
				h, e := url.Parse(v)
				if e != nil || h.Scheme != u.Scheme || h.Host != u.Host || h.User != nil || h.Fragment != "" {
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
	network := map[string]bool{}
	for _, c := range doc.Capabilities {
		network[c.ID] = containsString(c.Permissions, "network")
	}
	for _, r := range p.HTTPRoutes {
		if r.Credential != "" {
			field, ok := schema.Properties[r.Credential]
			if !ok || !field.Secret || !field.HostOnly || field.Type != "string" || len(field.Default) > 0 {
				return errors.New("HTTP route requires declared host-held secret")
			}
		}
		for _, c := range r.Capabilities {
			if !network[c] {
				return errors.New("HTTP route references missing network capability")
			}
		}
	}
	return nil
}

// ResolveHTTPRequest performs account/version/config/grant checks immediately
// before transport. The caller must additionally bind provider payload fields to
// the original authorized source; network grants alone do not prove that binding.
func (s *Store) ResolveHTTPRequest(ctx context.Context, j Job, cap Capability, route string, body json.RawMessage, referer ...string) (HostHTTPRequest, error) {
	var result HostHTTPRequest
	i, err := s.Installed(ctx, j.Plugin)
	if err != nil || !i.Enabled || i.Removed || j.Context.Account != s.account || j.Version != i.Package.Version || j.PackageDigest != i.Digest || cap.ID != j.Tool || !containsString(cap.Permissions, "network") || !containsString(i.Permissions, "network") {
		return result, errors.New("HTTP route authorization changed")
	}
	if err = validateHTTPRoutes(i.Package); err != nil {
		return result, err
	}
	var selected *HTTPRoute
	for _, r := range i.Package.HTTPRoutes {
		if r.ID == route && containsString(r.Capabilities, cap.ID) {
			copy := r
			selected = &copy
			break
		}
	}
	if selected == nil {
		return result, errors.New("HTTP route unavailable for capability")
	}
	var object map[string]json.RawMessage
	if len(body) > 1<<20 || json.Unmarshal(body, &object) != nil || object == nil {
		return result, errors.New("HTTP route body requires JSON object within 1 MiB")
	}
	config, err := s.ConfigurationForExecution(ctx, j.Plugin)
	if err != nil || strconv.Itoa(config.Revision) != j.ConfigRevision {
		return result, errors.New("HTTP route configuration changed")
	}
	result = HostHTTPRequest{Endpoint: selected.Endpoint, Headers: map[string]string{}, Body: append(json.RawMessage(nil), body...)}
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
		result.Headers[selected.CredentialHeader] = secret
	}
	return result, nil
}
