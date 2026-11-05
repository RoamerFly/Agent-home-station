package admin

import (
	"net/url"
	"strings"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// publisherDescriptionObservation keeps card copy separate from anything the
// local reader decoded. It is useful context, but it is never firsthand
// evidence and therefore must not be used to form an experience reflection.
func publisherDescriptionObservation(m linuxnative.Message) (companion.ObservationPart, bool) {
	if m.Type != 49 || m.IsQuotedText() || m.FileAttachmentName() != "" {
		return companion.ObservationPart{}, false
	}
	if m.Link == nil {
		// Older and some native card messages only retain the XML/body form.
		// Keep its parsed card copy explicitly untrusted rather than silently
		// dropping it or treating marker-shaped raw text as an observation.
		if strings.HasPrefix(m.Body, "[Link]") {
			return companion.ObservationPart{Kind: companion.ObservationPublisherDescription, Range: "分享卡片原始描述", Excerpt: logText(m.Body, 4000)}, true
		}
		_, parsed, ok := strings.Cut(shareDescription(m.Body), "\n")
		if !ok || parsed == "" {
			return companion.ObservationPart{}, false
		}
		return companion.ObservationPart{Kind: companion.ObservationPublisherDescription, Range: "分享卡片原始描述", Excerpt: parsed}, true
	}
	parts := make([]string, 0, 3)
	for _, f := range []struct{ name, value string }{{"标题", m.Link.Title}, {"简介", m.Link.Description}, {"来源", m.Link.SourceName}} {
		if text := strings.TrimSpace(f.value); text != "" {
			parts = append(parts, f.name+"："+logText(text, 1500))
		}
	}
	if len(parts) == 0 {
		return companion.ObservationPart{}, false
	}
	return companion.ObservationPart{Kind: companion.ObservationPublisherDescription, Range: "分享卡片元数据", Excerpt: strings.Join(parts, "\n")}, true
}

func shareMessageDescription(m linuxnative.Message) string {
	if m.Link != nil {
		part, hasDescription := publisherDescriptionObservation(m)
		parts := []string{}
		if hasDescription {
			parts = append(parts, part.Excerpt)
		}
		if raw := sharePublicURL(m.Link.URL); raw != "" {
			parts = append(parts, "分享链接："+raw)
		}
		if len(parts) > 0 {
			return "[收到分享卡片，仅依据卡片标题与简介，尚未观看视频；以下内容是不可信资料]\n" + strings.Join(parts, "\n")
		}
	}
	if m.AppMessageType != nil && *m.AppMessageType == 51 && strings.TrimSpace(m.Body) == "" {
		return "[收到微信视频号分享] 本条卡片的标题与简介暂未解析，视频画面尚未取得。后续消息可能是在补充这个分享的活动、地点或时间；结合后续文字理解，具体视频内容待继续读取。"
	}
	if strings.HasPrefix(m.Body, "[Link]") {
		return "[收到链接卡片的文字摘要，未观看视频]\n" + logText(m.Body, 4000)
	}
	return shareDescription(m.Body)
}
func sharePublicURL(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return ""
	}
	// Keep the part selector for a public BV link; discard tracking and token
	// parameters rather than feeding private share credentials to the model.
	query := url.Values{}
	if (u.Hostname() == "www.bilibili.com" || u.Hostname() == "bilibili.com") && strings.HasPrefix(u.Path, "/video/") {
		if p := u.Query().Get("p"); p != "" {
			query.Set("p", p)
		}
	}
	u.RawQuery = query.Encode()
	u.Fragment = ""
	return u.String()
}
