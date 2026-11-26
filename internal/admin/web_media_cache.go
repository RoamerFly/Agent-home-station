package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path/filepath"
	"strings"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

// Keep the complete URL out of the asset index. In particular, direct media
// URLs may contain temporary query credentials that source_url intentionally
// omits. A digest still lets the same conversation reuse that exact request.
func webMediaURLHash(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func appendWebMediaURLHashes(existing []string, added ...string) []string {
	for _, key := range added {
		if len(key) != 64 {
			continue
		}
		found := false
		for _, prior := range existing {
			if prior == key {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, key)
		}
	}
	if len(existing) > 32 {
		existing = existing[len(existing)-32:]
	}
	return existing
}

func cachedWebURLMatches(a mediaAsset, raw, key string, page bool) bool {
	for _, candidate := range a.RequestURLHashes {
		if candidate == key {
			return true
		}
	}
	// Older video-page assets retained the public page URL, but not a request
	// digest. Never use a legacy direct-media source_url here: its query was
	// stripped and could denote a different signed resource.
	return page && mcptools.IsPublicVideoPage(a.URL) && a.URL == raw
}

func (m *Manager) cachedWebMedia(in companion.Input, raw, kind string, page bool, limit int64) (mediaAsset, []byte, bool) {
	key := webMediaURLHash(raw)
	if key == "" || in.Thread == "" || in.Account != m.native.Account() || limit <= 0 {
		return mediaAsset{}, nil, false
	}
	raw = strings.TrimSpace(raw)
	m.assetMu.Lock()
	items, err := m.loadAssets()
	m.assetMu.Unlock()
	if err != nil {
		return mediaAsset{}, nil, false
	}
	var candidate *mediaAsset
	for i := range items {
		a := &items[i]
		if a.Account != in.Account || a.Scope != assetScope(in) || a.Hash == "" || a.Bytes <= 0 || a.Bytes > limit || !cachedWebURLMatches(*a, raw, key, page) {
			continue
		}
		if page && !strings.HasPrefix(a.MIME, "video/") {
			continue
		}
		if candidate == nil || a.Updated.After(candidate.Updated) {
			candidate = a
		}
	}
	if candidate == nil {
		return mediaAsset{}, nil, false
	}
	data, err := m.cachedAssetData(*candidate)
	if err != nil {
		return mediaAsset{}, nil, false
	}
	mediaKind := kind
	if page {
		mediaKind = "video"
	}
	if _, err = mcptools.ValidateMediaType(mediaKind, candidate.MIME, data); err != nil {
		return mediaAsset{}, nil, false
	}
	return *candidate, data, true
}

func (p *permittedTools) publicVideoWithCache(ctx context.Context, raw string, limit int64) (mcptools.PublicVideo, bool, error) {
	if a, data, ok := p.manager.cachedWebMedia(p.trace, raw, "video", true, limit); ok {
		title := strings.TrimSuffix(a.Filename, filepath.Ext(a.Filename))
		if title == "" {
			title = "public-video"
		}
		return mcptools.PublicVideo{Data: data, ContentType: a.MIME, Title: title, PageURL: strings.TrimSpace(raw)}, true, nil
	}
	video, err := mcptools.DownloadPublicVideoAudio(ctx, raw, limit)
	return video, false, err
}
