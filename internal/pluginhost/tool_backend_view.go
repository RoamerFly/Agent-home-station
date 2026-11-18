package pluginhost

import (
	"net"
	"net/url"
	"strings"
)

// ToolBackendView contains display metadata only. Credentials and URL paths,
// queries and fragments are never part of the project-tool catalog.
type ToolBackendView struct {
	Name          string `json:"name"`
	Scope         string `json:"scope,omitempty"`
	Kind          string `json:"kind"`
	State         string `json:"state"`
	Origin        string `json:"origin,omitempty"`
	Model         string `json:"model,omitempty"`
	Credential    string `json:"credential,omitempty"`
	KeyConfigured bool   `json:"key_configured,omitempty"`
}

func (v *ToolBackendView) SetEndpoint(raw string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return
	}
	v.Origin = u.Scheme + "://" + u.Host
	if strings.EqualFold(u.Hostname(), "localhost") || net.ParseIP(u.Hostname()).IsLoopback() {
		v.Kind = "loopback"
	}
}
