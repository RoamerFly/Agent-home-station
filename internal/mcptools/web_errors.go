package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

type WebError struct {
	Code       string
	Status     int
	Attempts   int
	Stage      string
	RetryAfter time.Duration
}

func (e *WebError) Error() string { return e.Code }

// FailureDescription never includes URLs, request headers, response bodies,
// subprocess output or arbitrary provider error strings.
func FailureDescription(err error) string {
	var safe interface{ SafeToolFailure() (string, string) }
	if errors.As(err, &safe) {
		_, message := safe.SafeToolFailure()
		return message
	}
	text := failureSummary(err)
	var web *WebError
	if errors.As(err, &web) && web.Attempts > 0 {
		text += fmt.Sprintf("；同一只读请求尝试%d次", web.Attempts)
	}
	return text
}
func failureSummary(err error) string {
	var web *WebError
	if errors.As(err, &web) {
		switch web.Code {
		case "discovery_source_invalid":
			return "内容源类型或公开网址无效"
		case "discovery_feed_unavailable":
			return "内容源未返回可解析的公开列表；可能限流或接口结构变化，已保留其他来源"
		case "query_required":
			return "缺少有效的query搜索关键词，或关键词超过长度限制"
		case "network_timeout":
			return "连接或读取超时"
		case "network_closed":
			return "连接提前中断，未取得完整响应"
		case "network_unavailable":
			return "无法连接目标服务器"
		case "dns_failed":
			return "DNS解析失败"
		case "tls_failed":
			return "目标证书或TLS握手验证失败"
		case "request_cancelled":
			return "调用已取消"
		case "catalog_invalid":
			return "音频目录返回格式无效"
		case "search_invalid":
			return "搜索站点未返回可解析的结果"
		case "search_empty":
			return "没有匹配的公开搜索结果，可换关键词或来源"
		case "search_no_relevant_results":
			return "搜索返回了无关内容，备用来源也未取得相关结果；未把这些内容当作目标作品"
		case "redirect_limit":
			return "目标跳转次数超过限制"
		case "media_download_incomplete":
			return "媒体分段响应不完整或下载期间文件发生变化，未暂存/发送"
		case "audio_prepare_failed":
			return "音频解码或分段失败，请检查 FFmpeg 与源文件格式"
		case "audio_duration_limit":
			return "音频为空、时长不可读或超过 15 分钟；可选择更短资源或按文件发送"
		case "url_required":
			return "缺少 url 参数，请填写公开网页的完整 HTTP(S) 地址"
		case "invalid_url":
			return "网址格式无效；需要公开 HTTP(S) 地址，不能包含账号密码或片段标记"
		case "private_destination":
			return "目标解析到内网、回环或保留地址，已阻止访问"
		case "http_status":
			if web.Status == 401 {
				return "目标服务要求身份认证（HTTP 401）；请使用该服务已配置的专用工具，或换公开来源"
			}
			return fmt.Sprintf("目标网站返回 HTTP %d；可能需要登录、限制访问或网址已失效", web.Status)
		case "authenticated_service_endpoint":
			return "这是需要鉴权的服务接口，不是内容网页。搜索请使用web_search；读取网页请传搜索结果的真实内容网址，不要重复访问搜索API地址"
		case "response_too_large":
			return "下载内容超过当前类型的大小上限"
		case "media_limit_invalid":
			return "媒体读取容量参数无效；需在1字节至512 MiB之间，按实际文件大小和资源选择"
		case "no_readable_text":
			return "网页没有可提取的正文；可能依赖 JavaScript 或本身是媒体链接"
		case "not_text":
			return "目标是非文字内容；音视频直链请使用 web_media_download"
		case "media_type_mismatch":
			return "返回内容与指定媒体类型不符；请提供可直接下载的媒体文件地址，不能使用播放器或登录页面"
		case "video_prepare_failed":
			return "视频格式整理失败；请检查 FFmpeg 是否可用以及原视频是否可转换为 MP4"
		case "video_page_unsupported":
			return "该视频页面暂未接入专用解析，需要通用网页、浏览器或设备读取"
		case "share_requires_browser":
			return "未取得分享实际内容，需要浏览器渲染、登录或App读取；未按标题生成回复"
		case "share_content_unsupported":
			return "分享返回的内容类型暂不支持读取；未按标题生成回复"
		case "video_page_metadata_unavailable":
			return "未取得视频页面的有效播放资料；可能是页面结构变化、验证页或已下架"
		case "video_public_playback_unavailable":
			return "该视频未提供可直接使用的免登录公开播放文件，未尝试账号或付费访问"
		case "video_progressive_unavailable":
			return "页面未提供单个普通MP4文件；当前不合并流媒体分段或解密受保护视频"
		case "video_duration_limit":
			return "页面视频超过10分钟或时长无效，请选择更短的片段"
		case "video_part_unavailable":
			return "请求的视频分P不存在或编号无效"
		case "media_permission_denied":
			return "此媒体类型的发送权限尚未开启；请在工具与权限中配置"
		case "ability_disabled":
			return "工具能力已关闭；请检查联网与媒体权限设置"
		}
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "DNS 解析失败，请检查服务器网络与目标域名"
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
		return "连接或读取超时；本次没有重试"
	}
	if errors.Is(err, context.Canceled) {
		return "调用已取消"
	}
	// These are our own bounded validation messages, never remote error text.
	if err != nil && strings.Contains(err.Error(), "web_fetch URL exceeds") {
		return "网址超过 2048 字符"
	}
	return "工具连接或执行失败，请检查服务状态、参数与权限；本次没有重试"
}
