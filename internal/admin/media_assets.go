package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type assetOriginal struct {
	Data                []byte
	MIME, URL, Filename string
}
type mediaAsset struct {
	ID       string            `json:"id"`
	Scope    string            `json:"scope"`
	Account  string            `json:"account"`
	Title    string            `json:"title"`
	Filename string            `json:"filename"`
	MIME     string            `json:"mime"`
	Hash     string            `json:"hash,omitempty"`
	URL      string            `json:"source_url,omitempty"`
	Bytes    int64             `json:"bytes"`
	Sources  map[string]string `json:"sources"`
	Updated  time.Time         `json:"updated"`
}

func assetScope(in companion.Input) string {
	return companion.ScopeID(in.Channel, in.Account, in.Thread)
}
func (m *Manager) loadAssets() ([]mediaAsset, error) {
	b, err := os.ReadFile(filepath.Join(m.dir, "media-assets.json"))
	if errors.Is(err, os.ErrNotExist) {
		return []mediaAsset{}, nil
	}
	var items []mediaAsset
	if err != nil || len(b) > 4<<20 || json.Unmarshal(b, &items) != nil || len(items) > 1000 {
		return nil, errors.New("媒体素材索引无法读取")
	}
	return items, nil
}
func (m *Manager) saveAssets(items []mediaAsset) error {
	b, e := json.Marshal(items)
	if e != nil {
		return e
	}
	return writePrivate(filepath.Join(m.dir, "media-assets.json"), b)
}
func assetName(a assetOriginal) string {
	if safeMediaFilename(a.Filename) {
		return a.Filename
	}
	if u, e := url.Parse(a.URL); e == nil {
		name := path.Base(u.Path)
		if safeMediaFilename(name) && strings.Contains(name, ".") {
			return name
		}
	}
	ext := ".bin"
	if extensions, _ := mime.ExtensionsByType(strings.Split(a.MIME, ";")[0]); len(extensions) > 0 {
		ext = extensions[0]
	}
	return "downloaded-media" + ext
}
func (m *Manager) rememberAsset(in companion.Input, original assetOriginal, kind string, source linuxnative.UploadSource) (mediaAsset, error) {
	m.assetMu.Lock()
	defer m.assetMu.Unlock()
	var zero mediaAsset
	if in.Account != m.native.Account() || in.Thread == "" || len(original.Data) == 0 || len(original.Data) > 64<<20 {
		return zero, errors.New("媒体素材范围或大小无效")
	}
	items, e := m.loadAssets()
	if e != nil {
		return zero, e
	}
	hash := sha256.Sum256(original.Data)
	digest := hex.EncodeToString(hash[:])
	scope := assetScope(in)
	identity := sha256.Sum256([]byte(scope + "|" + digest))
	id := "asset-" + hex.EncodeToString(identity[:16])
	index := -1
	var total int64
	for i, a := range items {
		total += a.Bytes
		if a.ID == id {
			index = i
		}
	}
	if index < 0 && (len(items) >= 1000 || total+int64(len(original.Data)) > 512<<20) {
		return zero, errors.New("本地素材缓存已满，请清理不再使用的素材")
	}
	dir := filepath.Join(m.dir, "media-assets")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return zero, e
	}
	file := filepath.Join(dir, digest)
	if _, e = os.Stat(file); errors.Is(e, os.ErrNotExist) {
		if e = writePrivate(file, original.Data); e != nil {
			return zero, e
		}
	} else if e != nil {
		return zero, e
	}
	a := mediaAsset{ID: id, Scope: scope, Account: in.Account, Title: assetName(original), Filename: assetName(original), MIME: original.MIME, Hash: digest, Bytes: int64(len(original.Data)), Sources: map[string]string{kind: source.ID}, Updated: time.Now().UTC()}
	if u, e := url.Parse(original.URL); e == nil && u.Host != "" {
		u.User = nil
		if !(strings.EqualFold(u.Hostname(), "www.bilibili.com") || strings.EqualFold(u.Hostname(), "bilibili.com")) {
			u.RawQuery = ""
		}
		u.Fragment = ""
		a.URL = u.String()
	}
	if index >= 0 {
		a.Sources = items[index].Sources
		if a.Sources == nil {
			a.Sources = map[string]string{}
		}
		a.Sources[kind] = source.ID
		items[index] = a
	} else {
		items = append(items, a)
	}
	return a, m.saveAssets(items)
}
func (m *Manager) recentAssets(in companion.Input) ([]map[string]any, error) {
	m.assetMu.Lock()
	defer m.assetMu.Unlock()
	items, e := m.loadAssets()
	if e != nil {
		return nil, e
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Updated.Before(items[j].Updated) })
	out := []map[string]any{}
	cutoff := time.Now().Add(-time.Duration(m.Config().Companion.MemoryDays) * 24 * time.Hour)
	for i := len(items) - 1; i >= 0 && len(out) < 12; i-- {
		a := items[i]
		if a.Account != in.Account || a.Scope != assetScope(in) || a.Updated.Before(cutoff) {
			continue
		}
		formats := []string{}
		for _, kind := range []string{"voice", "file", "video", "image"} {
			if a.Sources[kind] != "" && companion.MediaPermitted(m.Config().Companion.Permissions, kind) {
				formats = append(formats, kind)
			}
		}
		if a.Hash != "" && companion.MediaPermitted(m.Config().Companion.Permissions, "file") && a.Sources["file"] == "" {
			formats = append(formats, "file")
		}
		if len(formats) > 0 {
			out = append(out, map[string]any{"asset_id": a.ID, "title": a.Title, "filename": a.Filename, "bytes": a.Bytes, "formats": formats, "original_cached": a.Hash != "", "updated": a.Updated, "source_url": a.URL})
		}
	}
	return out, nil
}
func (p *permittedTools) reuseAsset(ctx context.Context, id, kind string) (string, error) {
	if p.trace.Preview || !companion.MediaPermitted(p.manager.Config().Companion.Permissions, kind) {
		return "", errors.New("media_permission_denied")
	}
	m := p.manager
	m.assetMu.Lock()
	defer m.assetMu.Unlock()
	items, e := m.loadAssets()
	if e != nil {
		return "", e
	}
	index := -1
	for i, a := range items {
		if a.ID == id && a.Account == p.trace.Account && a.Scope == assetScope(p.trace) {
			index = i
			break
		}
	}
	if index < 0 {
		return "", errors.New("当前会话没有该素材")
	}
	a := items[index]
	if time.Since(a.Updated) > time.Duration(m.Config().Companion.MemoryDays)*24*time.Hour {
		return "", errors.New("素材已超过会话保留时限")
	}
	source := a.Sources[kind]
	if source == "" {
		if kind != "file" || len(a.Hash) != 64 {
			return "", errors.New("该素材没有所需格式，请根据真实来源准备")
		}
		digest, e := hex.DecodeString(a.Hash)
		if e != nil || len(digest) != 32 {
			return "", errors.New("素材校验值无效")
		}
		data, e := os.ReadFile(filepath.Join(m.dir, "media-assets", a.Hash))
		if e != nil || len(data) > 64<<20 || int64(len(data)) != a.Bytes {
			return "", errors.New("原始文件缓存不可用")
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.Hash {
			return "", errors.New("原始文件校验失败")
		}
		native, ok := m.native.(nativeTools)
		if !ok {
			return "", errors.New("附件后台不可用")
		}
		upload, e := native.Upload(ctx, a.Filename, a.MIME, bytes.NewReader(data), int64(len(data)))
		if e != nil {
			return "", e
		}
		if e = m.tools.upload(stagedUpload{UploadSource: upload, Account: p.trace.Account, Kind: kind}); e != nil {
			return "", e
		}
		source = upload.ID
		a.Sources[kind] = source
		a.Updated = time.Now().UTC()
		items[index] = a
		if e = m.saveAssets(items); e != nil {
			return "", e
		}
	}
	// The alias binds kind and current-conversation ownership, while SourceID
	// stays the native upload handle. It never exposes a host filesystem path.
	alias := a.ID + "-" + kind
	media := companion.GeneratedMedia{ID: alias, SourceID: source, AssetID: a.ID, Kind: kind, Title: a.Title}
	p.mediaMu.Lock()
	p.media[alias] = media
	p.mediaMu.Unlock()
	b, _ := json.Marshal(map[string]any{"media": []companion.GeneratedMedia{media}, "reused_existing": true, "network_downloads": 0, "instruction": "尚未发送。将 media.id 放入最终 reply.media_ids；已有素材已复用，无需重新搜索。"})
	return string(b), nil
}

// Import only a verified, owner-selected historic job. Legacy audio can be
// reused as its real WAV source; do not pretend the original MP3 still exists.
func (m *Manager) rememberLegacyAsset(in companion.Input, kind, source string) (mediaAsset, error) {
	m.assetMu.Lock()
	defer m.assetMu.Unlock()
	items, e := m.loadAssets()
	if e != nil {
		return mediaAsset{}, e
	}
	if kind == "file" {
		for _, a := range items {
			if a.Account == in.Account && a.Scope == assetScope(in) && a.Sources["file"] == source {
				return a, nil
			}
		}
	}
	upload, ok := m.tools.source(source, in.Account, kind)
	if !ok {
		return mediaAsset{}, errors.New("素材不属于当前账号")
	}
	scope := assetScope(in)
	hash := sha256.Sum256([]byte(scope + "|" + source))
	id := "asset-" + hex.EncodeToString(hash[:16])
	for _, a := range items {
		if a.ID == id {
			return a, nil
		}
	}
	if len(items) >= 1000 {
		return mediaAsset{}, errors.New("素材索引已满")
	}
	sources := map[string]string{kind: source}
	if kind == "voice" {
		sources["file"] = source
	}
	a := mediaAsset{ID: id, Scope: scope, Account: in.Account, Title: upload.Filename, Filename: upload.Filename, MIME: upload.ContentType, Bytes: upload.ByteLength, Sources: sources, Updated: time.Now().UTC()}
	return a, m.saveAssets(append(items, a))
}
