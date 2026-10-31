package mobile

import (
	"math"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"
)

// ValidStep validates the deterministic, locally observable part of a device
// plan. It does not grant authority: Control rechecks the current source,
// settings, task state, and observation immediately before bridge calls.
//
// Model JSON is decoded into map[string]any, so protocol numbers must be
// float64 values that represent exact integers. This rejects both fractional
// coordinates and hand-constructed non-JSON types instead of relying on a
// later bridge conversion.
func ValidStep(step Step, observation Observation) bool {
	if !utf8.ValidString(step.Summary) || utf8.RuneCountInString(step.Summary) > 800 {
		return false
	}
	args := step.Arguments
	if args == nil {
		args = map[string]any{}
	}
	switch step.Action {
	case "finish", "blocked", "wait", "back", "home":
		return len(args) == 0
	case "capture":
		return hasOnly(args, "seconds") && integerIn(args["seconds"], 2, 12)
	case "launch":
		packageName, ok := args["package"].(string)
		return ok && hasOnly(args, "package") && packageName != "" && contains(observation.AllowedPackages, packageName)
	case "open_url":
		packageName, packageOK := args["package"].(string)
		address, addressOK := args["url"].(string)
		return packageOK && addressOK && hasOnly(args, "package", "url") && packageName != "" && contains(observation.Browsers, packageName) && validPublicHTTPSURL(address, observation.WebHosts)
	case "text":
		text, ok := args["text"].(string)
		return ok && hasOnly(args, "text") && text != "" && utf8.ValidString(text) && utf8.RuneCountInString(text) <= 200
	case "tap":
		return hasOnly(args, "x", "y") && coordinate(args["x"], observation.Width) && coordinate(args["y"], observation.Height)
	case "swipe":
		if !hasOnly(args, "x", "y", "x2", "y2", "duration_ms") || !coordinate(args["x"], observation.Width) || !coordinate(args["y"], observation.Height) || !coordinate(args["x2"], observation.Width) || !coordinate(args["y2"], observation.Height) {
			return false
		}
		// duration_ms is optional in the bridge protocol. When present, retain
		// its documented 100..1500 ms range rather than narrowing it to a host
		// preference.
		if value, present := args["duration_ms"]; present && !integerIn(value, 100, 1500) {
			return false
		}
		return true
	default:
		return false
	}
}

func hasOnly(args map[string]any, permitted ...string) bool {
	if len(args) != len(permitted) {
		// Swipe's duration_ms is intentionally optional.
		if !(len(permitted) == 5 && len(args) == 4 && args["duration_ms"] == nil) {
			return false
		}
	}
	for key := range args {
		found := false
		for _, allowed := range permitted {
			if key == allowed {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func integerIn(value any, min, max int) bool {
	number, ok := value.(float64)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number == math.Trunc(number) && number >= float64(min) && number <= float64(max)
}

func coordinate(value any, extent int) bool {
	return extent > 0 && integerIn(value, 0, extent-1)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func validPublicHTTPSURL(value string, allowedHosts []string) bool {
	if value == "" || len(value) > 8192 || !utf8.ValidString(value) {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || (parsed.Port() != "" && parsed.Port() != "443") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || !validPublicHostSyntax(host) || !containsHost(allowedHosts, host) {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()) {
		return false
	}
	return true
}

func validPublicHostSyntax(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	// DNS resolution is deliberately deferred to the bridge, but a single-label
	// or localhost name is not a public web host at this protocol boundary.
	if len(host) > 253 || host == "localhost" || strings.HasSuffix(host, ".localhost") || !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
		}
	}
	return true
}

func containsHost(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
