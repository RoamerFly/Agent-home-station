package mcptools

import (
	"html"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var mediaAttribute = regexp.MustCompile(`(?is)(?:src|href|content)\s*=\s*["']([^"']+)["']`)

// ExtractMediaLinks advertises bounded public candidates from the fetched page.
// It does not fetch them; the download tool validates DNS and every redirect.
func ExtractMediaLinks(body, base string) []string {
	u, err := url.Parse(base)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, match := range mediaAttribute.FindAllStringSubmatch(body, 2000) {
		r, err := url.Parse(html.UnescapeString(match[1]))
		if err != nil {
			continue
		}
		r = u.ResolveReference(r)
		r.Fragment = ""
		switch strings.ToLower(path.Ext(r.Path)) {
		case ".mp3", ".wav", ".ogg", ".m4a", ".aac", ".flac", ".mp4", ".webm", ".mov", ".jpg", ".jpeg", ".png", ".gif", ".webp":
		default:
			continue
		}
		candidate := r.String()
		if len(candidate) > 2048 || validPublicURL(candidate) != nil || seen[candidate] {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
		if len(out) >= 12 {
			break
		}
	}
	return out
}

func ValidateMediaType(kind, contentType string, data []byte) (string, error) {
	t := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	sniff := http.DetectContentType(data)
	if strings.HasPrefix(sniff, "text/") || t == "text/html" || strings.Contains(t, "json") {
		return "", &WebError{Code: "media_type_mismatch"}
	}
	if t == "" || t == "application/octet-stream" {
		t = sniff
	}
	valid := false
	switch kind {
	case "voice":
		valid = strings.HasPrefix(t, "audio/") || t == "application/ogg"
	case "video":
		valid = strings.HasPrefix(t, "video/")
	case "image":
		valid = strings.HasPrefix(t, "image/")
	case "file":
		valid = true
	}
	if !valid {
		return "", &WebError{Code: "media_type_mismatch"}
	}
	return t, nil
}
