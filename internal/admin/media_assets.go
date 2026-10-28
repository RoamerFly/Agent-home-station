package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type assetOriginal struct {
	Music               *songSpec
	Data                []byte
	MIME, URL, Filename string
	Parent              *assetOriginal
}
type assetRequest struct {
	MessageTime   string    `json:"message_time,omitempty"`
	TraceID       string    `json:"trace_id,omitempty"`
	RequesterID   string    `json:"requester_id,omitempty"`
	RequesterName string    `json:"requester_name,omitempty"`
	TaskID        string    `json:"task_id,omitempty"`
	Text          string    `json:"request,omitempty"`
	At            time.Time `json:"at"`
}
type assetFormat struct {
	Batch   string   `json:"batch"`
	Sources []string `json:"sources"`
}
type mediaAsset struct {
	Music         *songSpec              `json:"music_generation,omitempty"`
	FirstRequest  *assetRequest          `json:"first_request,omitempty"`
	ParentAssetID string                 `json:"parent_asset_id,omitempty"`
	Requests      []assetRequest         `json:"requests,omitempty"`
	Formats       map[string]assetFormat `json:"formats,omitempty"`
	ID            string                 `json:"id"`
	Scope         string                 `json:"scope"`
	Account       string                 `json:"account"`
	Title         string                 `json:"title"`
	Filename      string                 `json:"filename"`
	MIME          string                 `json:"mime"`
	Hash          string                 `json:"hash,omitempty"`
	URL           string                 `json:"source_url,omitempty"`
	Bytes         int64                  `json:"bytes"`
	Sources       map[string]string      `json:"sources"`
	Updated       time.Time              `json:"updated"`
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
	if err != nil || len(b) > 16<<20 || json.Unmarshal(b, &items) != nil {
		return nil, errors.New("媒体素材索引无法读取")
	}
	return items, nil
}
func (m *Manager) saveAssets(items []mediaAsset) error {
	b, e := json.Marshal(items)
	if e != nil {
		return e
	}
	if len(b) > 16<<20 {
		return errors.New("媒体素材索引超过容量限制")
	}
	return writePrivate(filepath.Join(m.dir, "media-assets.json"), b)
}
func assetRequestFor(in companion.Input) assetRequest {
	trace := in.OriginTraceID
	if trace == "" {
		trace = in.TraceID
	}
	text := in.Text
	if in.TaskID != "" && in.RequestedText != "" {
		text = in.RequestedText
	}
	return assetRequest{MessageTime: in.MessageTime, TraceID: logText(trace, 256), RequesterID: logText(in.Sender, 256), RequesterName: logText(in.SenderName, 128), TaskID: logText(in.TaskID, 256), Text: logText(companion.UserRequestText(text), 320), At: time.Now().UTC()}
}
func (a *mediaAsset) noteRequest(in companion.Input) {
	r := assetRequestFor(in)
	if r.TraceID == "" && r.RequesterID == "" && r.TaskID == "" {
		return
	}
	if a.FirstRequest == nil {
		first := r
		a.FirstRequest = &first
	}
	for i := range a.Requests {
		if a.Requests[i].TraceID == r.TraceID && a.Requests[i].RequesterID == r.RequesterID && a.Requests[i].TaskID == r.TaskID {
			return // Repeated voice parts and format conversion keep one request.
		}
	}
	a.Requests = append(a.Requests, r)
	if len(a.Requests) > 8 {
		a.Requests = a.Requests[len(a.Requests)-8:]
	}
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
	return m.rememberAssetFormats(in, original, []stagedUpload{{UploadSource: source, Kind: kind}})
}

// Commit every upload material of a format together, so concurrent requests
// never see a half-prepared replacement bundle.
func (m *Manager) rememberAssetFormats(in companion.Input, original assetOriginal, uploads []stagedUpload) (mediaAsset, error) {
	parentID := ""
	if original.Parent != nil {
		parent := *original.Parent
		parent.Parent = nil // A conversion carries one verified parent, not recursion.
		cached, err := m.rememberAssetFormats(in, parent, nil)
		if err != nil {
			return mediaAsset{}, err
		}
		parentID = cached.ID
	}
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
	if parentID == id {
		parentID = ""
	}
	index := -1
	var total int64
	for i, a := range items {
		total += a.Bytes
		if a.ID == id {
			index = i
		}
	}
	if index < 0 && total+int64(len(original.Data)) > 512<<20 {
		return zero, errors.New("本地素材缓存已满，请清理不再使用的素材")
	}
	dir := m.assetDirectory()
	if e = os.MkdirAll(dir, 0700); e != nil {
		return zero, e
	}
	if e = m.writeManagedAsset(digest, original.Data); e != nil {
		return zero, e
	}
	a := mediaAsset{ID: id, Scope: scope, Account: in.Account, Title: assetName(original), Filename: assetName(original), MIME: original.MIME, Hash: digest, Bytes: int64(len(original.Data)), Sources: map[string]string{}, Updated: time.Now().UTC()}
	if u, e := url.Parse(original.URL); e == nil && u.Host != "" {
		u.User = nil
		if !(strings.EqualFold(u.Hostname(), "www.bilibili.com") || strings.EqualFold(u.Hostname(), "bilibili.com")) {
			u.RawQuery = ""
		}
		u.Fragment = ""
		a.URL = u.String()
	}
	if index >= 0 {
		a.Music = items[index].Music
		a.FirstRequest = items[index].FirstRequest
		if parentID == "" {
			parentID = items[index].ParentAssetID
		}
		if a.URL == "" {
			a.URL = items[index].URL
		}
		a.Requests = items[index].Requests
		a.Formats = items[index].Formats
		a.Sources = items[index].Sources
		if a.Sources == nil {
			a.Sources = map[string]string{}
		}
	}
	if original.Music != nil {
		spec := *original.Music
		a.Music = &spec
	}
	a.ParentAssetID = parentID
	a.noteRequest(in)
	if a.Formats == nil {
		a.Formats = map[string]assetFormat{}
	}
	formats := map[string]assetFormat{}
	for _, upload := range uploads {
		if upload.Kind == "" || upload.ID == "" {
			continue
		}
		format := formats[upload.Kind]
		format.Batch = in.TraceID
		if format.Batch == "" {
			format.Batch = upload.ID
		}
		found := false
		for _, id := range format.Sources {
			found = found || id == upload.ID
		}
		if !found {
			format.Sources = append(format.Sources, upload.ID)
		}
		formats[upload.Kind] = format
		a.Sources[upload.Kind] = upload.ID
	}
	for kind, format := range formats {
		a.Formats[kind] = format
	}

	if index >= 0 {
		items[index] = a
	} else {
		items = append(items, a)
	}
	return a, m.saveAssets(items)
}
func (m *Manager) recentAssets(in companion.Input) ([]map[string]any, error) {
	return m.findAssets(in, "", "", "")
}
func (m *Manager) findAssets(in companion.Input, assetID, requester, query string) ([]map[string]any, error) {
	rows, _, err := m.findAssetPage(in, assetID, requester, query, 0, 12)
	return rows, err
}
func (m *Manager) findAssetPage(in companion.Input, assetID, requester, query string, offset, limit int) ([]map[string]any, int, error) {
	m.assetMu.Lock()
	defer m.assetMu.Unlock()
	items, e := m.loadAssets()
	if e != nil {
		return nil, 0, e
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Updated.Before(items[j].Updated) })
	out := []map[string]any{}
	cutoff := time.Now().Add(-time.Duration(m.Config().Companion.MemoryDays) * 24 * time.Hour)
	for i := len(items) - 1; i >= 0; i-- {
		a := items[i]
		if a.Account != in.Account || a.Scope != assetScope(in) || a.Updated.Before(cutoff) {
			continue
		}
		if assetID != "" && a.ID != assetID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(a.Title+" "+a.Filename), strings.ToLower(query)) {
			continue
		}
		if requester != "" {
			match := a.FirstRequest != nil && a.FirstRequest.RequesterID == requester
			for _, r := range a.Requests {
				match = match || r.RequesterID == requester
			}
			if !match {
				continue
			}
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
		if len(formats) > 0 || a.Hash != "" && strings.HasPrefix(a.MIME, "video/") && m.Config().Behavior.ReadVideos {
			requests := []map[string]string{}
			for _, r := range a.Requests[max(0, len(a.Requests)-2):] {
				requests = append(requests, assetRequestSummary(r))
			}
			var first any
			if a.FirstRequest != nil {
				first = assetRequestSummary(*a.FirstRequest)
			}
			row := map[string]any{"asset_id": a.ID, "parent_asset_id": a.ParentAssetID, "title": a.Title, "filename": a.Filename, "bytes": a.Bytes, "formats": formats, "original_cached": a.Hash != "", "cached_mime": a.MIME, "updated": a.Updated, "source_url": a.URL, "first_request": first, "requests": requests}
			if a.Music != nil {
				row["generation_mode"], row["reference_asset_id"], row["seed"] = a.Music.Mode, a.Music.Reference, a.Music.Seed
			}
			out = append(out, row)
		}
	}
	if offset < 0 || offset > len(out) || limit < 1 {
		return nil, 0, errors.New("素材分页参数无效")
	}
	return out[offset : offset+min(limit, len(out)-offset)], len(out), nil
}
func assetRequestSummary(r assetRequest) map[string]string {
	return map[string]string{"trace_id": r.TraceID, "requester_id": r.RequesterID, "requester_name": r.RequesterName, "task_id": r.TaskID}
}
func (p *permittedTools) reuseAsset(ctx context.Context, id, kind string) (string, error) {
	if p.trace.Preview || !companion.MediaPermitted(p.manager.Config().Companion.Permissions, kind) {
		return "", errors.New("media_permission_denied")
	}
	m := p.manager
	m.assetMu.Lock()
	locked := true
	defer func() {
		if locked {
			m.assetMu.Unlock()
		}
	}()
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
	if kind == "voice" && len(a.Formats[kind].Sources) == 0 && a.Hash != "" && strings.HasPrefix(a.MIME, "audio/") {
		// Old indexes retained only the last voice material. Rebuild the
		// complete sequence locally from the verified full original instead.
		data, err := m.cachedAssetData(a)
		if err != nil {
			return "", err
		}
		m.assetMu.Unlock()
		locked = false
		output, err := preparePublicAudioPreview(ctx, m.dir, data, 0)
		if err != nil {
			return "", err
		}
		result, err := p.stageOutputWithAsset(ctx, output, &assetOriginal{Data: data, MIME: a.MIME, Filename: a.Filename, URL: a.URL})
		if err != nil {
			return "", err
		}
		var value map[string]any
		if json.Unmarshal([]byte(result), &value) != nil {
			return "", errors.New("重建语音结果无效")
		}
		value["reused_existing"], value["rebuilt_legacy_voice"], value["network_downloads"] = true, true, 0
		b, _ := json.Marshal(value)
		return string(b), nil
	}
	source := a.Sources[kind]
	if source == "" {
		if kind != "file" || len(a.Hash) != 64 {
			if a.ParentAssetID != "" {
				for _, parent := range items {
					if parent.ID == a.ParentAssetID && parent.Account == p.trace.Account && parent.Scope == assetScope(p.trace) {
						b, _ := json.Marshal(map[string]any{"media": []companion.GeneratedMedia{}, "requested_format_unavailable": true, "asset_id": a.ID, "cached_mime": a.MIME, "parent_asset_id": parent.ID, "parent_cached_mime": parent.MIME, "parent_filename": parent.Filename, "source_url": a.URL, "parent_original_cached": parent.Hash != "", "instruction": "没有执行下载或发送；当前素材没有所选格式。父素材已在本会话缓存。若用户要原视频文件，请调用media_reuse(asset_id=parent_asset_id,kind=file)；不要重新下载同一资源。根据用户要求自行选择正确素材和格式。"})
						return string(b), nil
					}
				}
			}
			if a.Hash != "" || a.Sources["file"] != "" {
				b, _ := json.Marshal(map[string]any{"media": []companion.GeneratedMedia{}, "requested_format_unavailable": true, "asset_id": a.ID, "cached_mime": a.MIME, "filename": a.Filename, "file_available": true, "source_url": a.URL, "instruction": "当前素材没有所选的发送格式，但本会话保留了原文件。若用户要原文件，请用相同asset_id和kind=file；不是必须重新下载。请根据用户要求和cached_mime自行选择，未执行发送。"})
				return string(b), nil
			}
			return "", errors.New("该素材没有所需格式，请根据真实来源准备")
		}
		native, ok := m.native.(nativeTools)
		if !ok {
			return "", errors.New("附件后台不可用")
		}
		caps, e := native.Capabilities(ctx)
		if e != nil {
			return "", e
		}
		if caps.MediaSendMaxBytes[kind] <= 0 || a.Bytes > caps.MediaSendMaxBytes[kind] {
			return "", errors.New("原件大小超过当前微信后台能力")
		}
		upload, e := m.uploadVerifiedAsset(ctx, native, a)
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
	sources := []string{source}
	if format := a.Formats[kind]; len(format.Sources) > 0 {
		sources = format.Sources
	}
	itemsOut := []companion.GeneratedMedia{}
	p.mediaMu.Lock()
	for i, id := range sources {
		alias := a.ID + "-" + kind
		if len(sources) > 1 {
			alias += "-" + strconv.Itoa(i+1)
		}
		media := companion.GeneratedMedia{ID: alias, SourceID: id, AssetID: a.ID, ParentAssetID: a.ParentAssetID, Kind: kind, Title: a.Title, URL: a.URL, CachedMIME: a.MIME}
		p.media[alias] = media
		itemsOut = append(itemsOut, media)
	}
	p.mediaMu.Unlock()
	a.noteRequest(p.trace)
	items[index] = a
	if err := m.saveAssets(items); err != nil {
		return "", err
	}
	required := []string{}
	if kind == "voice" && len(itemsOut) > 1 {
		for _, item := range itemsOut {
			required = append(required, item.ID)
		}
	}
	b, _ := json.Marshal(map[string]any{"required_media_ids": required, "media": itemsOut, "reused_existing": true, "network_downloads": 0, "cached_mime": a.MIME, "source_url": a.URL, "instruction": "尚未发送。将所需的全部 media.id 按顺序放入最终 reply.media_ids；已有素材已复用，无需重新搜索。cached_mime是实际缓存文件格式，音轨不是原视频；若要原视频而仅缓存音轨，应使用source_url取得视频，不把音频文件冒充视频。"})
	return string(b), nil
}

func (m *Manager) cachedAssetData(a mediaAsset) ([]byte, error) {
	digest, err := hex.DecodeString(a.Hash)
	if err != nil || len(a.Hash) != 64 || len(digest) != 32 {
		return nil, errors.New("素材校验值无效")
	}
	f, err := m.openAsset(a.Hash)
	if err != nil {
		return nil, errors.New("原始文件缓存不可用")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if err != nil || len(data) > 64<<20 || int64(len(data)) != a.Bytes {
		return nil, errors.New("原始文件缓存不可用")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != a.Hash {
		return nil, errors.New("原始文件校验失败")
	}
	return data, nil
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
	a.noteRequest(in)
	return a, m.saveAssets(append(items, a))
}
