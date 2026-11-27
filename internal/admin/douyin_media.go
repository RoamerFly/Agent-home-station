package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"wechat-bot/internal/mcptools"
)

const stationDouyinMediaLimit = 32 << 20

var douyinShareVideoPath = regexp.MustCompile(`^/share/video/([0-9]{10,25})/?$`)
var douyinMediaID = regexp.MustCompile(`^[0-9]{10,25}$`)

// A Douyin video page needs the saved browser profile. Treating its HTML as a
// direct media URL only repeats a failed download and a failed web_fetch.
func isDouyinVideoLink(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "v.douyin.com", "www.douyin.com", "douyin.com", "www.iesdouyin.com":
		return true
	default:
		return false
	}
}

func resolveDouyinVideoPage(ctx context.Context, raw string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", &mcptools.WebError{Code: "invalid_url"}
	}
	if u.Scheme == "https" && u.User == nil && u.Port() == "" && u.Fragment == "" {
		switch strings.ToLower(u.Hostname()) {
		case "www.douyin.com", "douyin.com", "www.iesdouyin.com":
			if match := douyinShareVideoPath.FindStringSubmatch(u.Path); match != nil {
				return "https://www.douyin.com/video/" + match[1], match[1], nil
			}
		}
	}
	return mcptools.ResolveDouyinLink(ctx, raw)
}

type douyinMediaFailure struct {
	code, message string
	status        int
}

func (e *douyinMediaFailure) Error() string { return e.code }
func (e *douyinMediaFailure) SafeToolFailure() (string, string) {
	return e.code, e.message
}
func (e *douyinMediaFailure) Unwrap() error {
	return &mcptools.WebError{Code: e.code, Status: e.status, Stage: "station"}
}

func douyinFailure(code, message string, status int) error {
	return &douyinMediaFailure{code: code, message: message, status: status}
}

func publicVideoSourceFilename(title, mediaType string) string {
	extension := ".mp4"
	if mediaType == "video/webm" {
		extension = ".webm"
	}
	filename := title + extension
	if !safeMediaFilename(filename) {
		return "public-video" + extension
	}
	return filename
}

func (p *permittedTools) douyinVideoWithCache(ctx context.Context, raw string, limit int64) (mcptools.PublicVideo, bool, error) {
	if limit <= 0 {
		return mcptools.PublicVideo{}, false, &mcptools.WebError{Code: "media_limit_invalid"}
	}
	limit = stationDouyinMediaLimit // Limit the source video independently of a voice/file output.
	if asset, data, ok := p.manager.cachedWebMedia(p.trace, raw, "video", true, limit); ok {
		return mcptools.PublicVideo{Data: data, ContentType: asset.MIME, Title: strings.TrimSuffix(asset.Filename, filepath.Ext(asset.Filename)), PageURL: asset.URL}, true, nil
	}
	page, id, err := resolveDouyinVideoPage(ctx, raw)
	if err != nil {
		return mcptools.PublicVideo{}, false, err
	}
	if asset, data, ok := p.manager.cachedWebMedia(p.trace, page, "video", true, limit); ok {
		return mcptools.PublicVideo{Data: data, ContentType: asset.MIME, Title: strings.TrimSuffix(asset.Filename, filepath.Ext(asset.Filename)), PageURL: page}, true, nil
	}
	data, typ, err := p.stationDouyinVideo(ctx, id)
	if err != nil {
		return mcptools.PublicVideo{}, false, err
	}
	return mcptools.PublicVideo{Data: data, ContentType: typ, Title: id, PageURL: page}, false, nil
}

func (p *permittedTools) stationDouyinVideo(ctx context.Context, id string) ([]byte, string, error) {
	if !douyinMediaID.MatchString(id) {
		return nil, "", &mcptools.WebError{Code: "video_page_unsupported"}
	}
	session, err := p.stationToolLogin(ctx)
	if err != nil {
		return nil, "", douyinFailure("douyin_station_unavailable", "工作站的抖音视频读取服务暂不可用", 0)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, session.BaseURL+"/api/local/apps/douyin/media/"+id, nil)
	if err != nil {
		return nil, "", douyinFailure("douyin_station_unavailable", "工作站的抖音视频读取服务暂不可用", 0)
	}
	request.Header.Set("Origin", session.BaseURL)
	request.Header.Set("Cookie", session.Cookie)
	response, err := (&http.Client{Timeout: 110 * time.Second}).Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", douyinFailure("douyin_station_unavailable", "工作站的抖音视频读取服务暂不可用", 0)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var detail struct {
			Error string `json:"error"`
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		_ = json.Unmarshal(body, &detail)
		switch {
		case response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusLocked || strings.Contains(strings.ToLower(detail.Error), "paused") || strings.Contains(detail.Error, "暂停"):
			return nil, "", douyinFailure("douyin_profile_paused", "抖音网页连接已暂停，可在抖音工作台恢复后重试", response.StatusCode)
		case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
			return nil, "", douyinFailure("douyin_station_unauthorized", "工作站未授权读取该抖音视频", response.StatusCode)
		default:
			return nil, "", douyinFailure("douyin_video_unavailable", "抖音工作台未取得该作品的视频文件", response.StatusCode)
		}
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "video/mp4" && mediaType != "video/webm" {
		return nil, "", douyinFailure("douyin_media_type_invalid", "抖音工作台返回的不是视频文件", response.StatusCode)
	}
	if response.ContentLength <= 0 || response.ContentLength > stationDouyinMediaLimit {
		return nil, "", &mcptools.WebError{Code: "response_too_large", Stage: "station"}
	}
	claimedHash := strings.ToLower(strings.TrimSpace(response.Header.Get("X-Content-SHA256")))
	if len(claimedHash) != 64 {
		return nil, "", douyinFailure("douyin_media_digest_missing", "抖音视频缺少完整性校验信息", response.StatusCode)
	}
	if _, err := hex.DecodeString(claimedHash); err != nil {
		return nil, "", douyinFailure("douyin_media_digest_invalid", "抖音视频完整性校验信息无效", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, stationDouyinMediaLimit+1))
	if err != nil {
		return nil, "", douyinFailure("douyin_media_incomplete", "抖音视频下载中断，未准备微信附件", response.StatusCode)
	}
	if int64(len(data)) != response.ContentLength || len(data) > stationDouyinMediaLimit {
		return nil, "", douyinFailure("douyin_media_incomplete", "抖音视频下载不完整，未准备微信附件", response.StatusCode)
	}
	hash := sha256.Sum256(data)
	if fmt.Sprintf("%x", hash) != claimedHash {
		return nil, "", douyinFailure("douyin_media_digest_mismatch", "抖音视频完整性校验失败，未准备微信附件", response.StatusCode)
	}
	if _, err = mcptools.ValidateMediaType("video", mediaType, data); err != nil {
		return nil, "", douyinFailure("douyin_media_type_invalid", "抖音工作台返回的不是视频文件", response.StatusCode)
	}
	validHeader := mediaType == "video/mp4" && len(data) >= 12 && string(data[4:8]) == "ftyp" ||
		mediaType == "video/webm" && len(data) >= 4 && string(data[:4]) == "\x1a\x45\xdf\xa3"
	if !validHeader {
		return nil, "", douyinFailure("douyin_media_type_invalid", "抖音视频文件类型与内容不符", response.StatusCode)
	}
	return data, mediaType, nil
}
