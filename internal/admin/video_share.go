package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"wechat-bot/internal/companion"
)

func (p *permittedTools) stageVideoShare(page, title string, durationMS int64) (string, error) {
	digest := sha256.Sum256([]byte(p.trace.Account + "|" + p.trace.Thread + "|" + page))
	id := "share-" + hex.EncodeToString(digest[:20])
	seconds := durationMS / 1000
	description := fmt.Sprintf("视频时长 %d分%02d秒，点击打开播放页", seconds/60, seconds%60)
	item := companion.GeneratedMedia{DurationMS: durationMS, ID: id, Kind: "share", Title: logText(title, 700), URL: page, Description: description}
	p.mediaMu.Lock()
	p.media[id] = item
	p.mediaMu.Unlock()
	b, _ := json.Marshal(map[string]any{"media": []companion.GeneratedMedia{item}, "duration_ms": durationMS, "link_only": true, "metadata_only": true, "video_frames_observed": false, "download_bytes": 0, "instruction": "视频超过一分钟，已准备微信可点击分享卡片，尚未发送。将此media.id放进reply.media_ids；不要再下载整段视频。当前观察范围：标题和时长；画面、动作和对白待读取。可以按真实标题表达兴趣，自然分享。结果说明将等卡片获得微信接受回执后发送。"})
	return string(b), nil
}
