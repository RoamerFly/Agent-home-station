package pluginhost

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestToolBackendViewReportsOriginWithoutCredentials(t *testing.T) {
	view := ToolBackendView{Kind: "api", Credential: "api_key", KeyConfigured: true}
	endpoint := url.URL{Scheme: "https", Host: "models.example:8443", User: url.UserPassword("user", "private-pass"), Path: "/path-token", RawQuery: "key=query-token", Fragment: "fragment-token"}
	view.SetEndpoint(endpoint.String())
	if view.Origin != "https://models.example:8443" || view.Kind != "api" {
		t.Fatal(view)
	}
	raw, err := json.Marshal(view)
	if err != nil || strings.Contains(string(raw), "token") || strings.Contains(string(raw), "private-pass") {
		t.Fatal("catalog contains authentication material")
	}
	view.SetEndpoint("http://[::1]:8088/v1")
	if view.Origin != "http://[::1]:8088" || view.Kind != "loopback" {
		t.Fatal(view)
	}
}
