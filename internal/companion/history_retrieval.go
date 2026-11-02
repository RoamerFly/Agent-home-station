package companion

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const historyRetrievalPolicy = "\n【按需读原始记录】近期上下文和压缩摘要只是选取窗口，不是历史是否存在的依据。需要之前的消息/图片/视频时，先memory_search(originals=true)找来源，再memory_read(event=true)分页读原文或part=observation读保存的观察，part=transcript只读已有音轨正文；不足时wechat_read查原生历史、video_inspect补读素材。群内说这条之前的视频，未明确指定作者作品时优先按本群消息时间、引用原消息为锚点，不擅自改成作者作品列表。仍有明确歧义时简短确认范围，不要求用户搬运已有资料。空上传正文不表示没有观察内容，先看available_parts。未尝试读取不要求用户重发；缺失、读取失败、部分覆盖和确实不存在分开说明。"

var previousVideos = regexp.MustCompile(`(?:前面|前|之前的?|此前的?)([0-9一二两三四五六七八九十百]+)(?:条|个|段)视频`)

type historyMaterialNeed struct {
	ExplicitConversation bool
	Anchor               string
	Count                int
	IDs                  map[string]bool
	MessageIDs           map[string]string
	ReadIDs              map[string]bool
	ContentRead          bool
	ReadFailed           bool
	Prompted             bool
	Events               map[string]MemoryEvent
	ReadPackets          map[string]string // exact tool feedback -> verified directory event
}

func historyCount(text string) int {
	if n, err := strconv.Atoi(text); err == nil {
		return n
	}
	value, digit := 0, 0
	for _, r := range text {
		if r == '十' || r == '百' {
			if digit == 0 {
				digit = 1
			}
			unit := 10
			if r == '百' {
				unit = 100
			}
			value += digit * unit
			digit = 0
			continue
		}
		switch r {
		case '一':
			digit = 1
		case '二', '两':
			digit = 2
		case '三':
			digit = 3
		case '四':
			digit = 4
		case '五':
			digit = 5
		case '六':
			digit = 6
		case '七':
			digit = 7
		case '八':
			digit = 8
		case '九':
			digit = 9
		default:
			return 0
		}
	}
	return value + digit
}

// Narrow, read-only prefetch for explicit numbered previous-video requests.
// It never selects a creator's feed or searches another conversation.
func requestedHistory(turns []map[string]string) *historyMaterialNeed {
	for i := len(turns) - 1; i >= 0; i-- {
		var p struct {
			Kind, Text string
			IsGroup    bool       `json:"is_group"`
			Source     *SourceRef `json:"source"`
			Quoted     *SourceRef `json:"quoted_source"`
		}
		if json.Unmarshal([]byte(turns[i]["content"]), &p) != nil || p.Kind != "current_message" {
			continue
		}
		p.Text = UserRequestText(p.Text)
		for _, s := range []string{"不要读取前", "不要查前", "别查前", "别读取前", "不用读取前", "无需读取前", "不要总结前", "别总结前", "作者", "主页", "作品列表", "视频号里", "该账号", "这个账号"} {
			if strings.Contains(p.Text, s) {
				return nil
			}
		}
		m := previousVideos.FindStringSubmatch(strings.ReplaceAll(p.Text, " ", ""))
		if len(m) == 0 {
			return nil
		}
		count := historyCount(m[1])
		if count < 1 {
			return nil
		}
		anchor := ""
		if p.Quoted != nil {
			anchor = p.Quoted.MessageID
		}
		if anchor == "" && p.Source != nil {
			anchor = p.Source.MessageID
		}
		if anchor == "" {
			return nil
		}
		explicit := p.IsGroup
		for _, word := range []string{"本群", "群里", "群内", "聊天记录", "发过的", "分享过的"} {
			explicit = explicit || strings.Contains(p.Text, word)
		}
		return &historyMaterialNeed{ExplicitConversation: explicit, Anchor: anchor, Count: count, IDs: map[string]bool{}, MessageIDs: map[string]string{}, ReadIDs: map[string]bool{}}
	}
	return nil
}

func (h *historyMaterialNeed) rememberDirectory(result string) {
	var p MemoryPage
	if json.Unmarshal([]byte(result), &p) != nil {
		return
	}
	if h.Events == nil {
		h.Events = map[string]MemoryEvent{}
	}
	for _, item := range p.Items {
		if item.Event != nil && item.Event.Trace != h.Anchor {
			h.IDs[item.Event.ID] = true
			h.MessageIDs[item.Event.Trace] = item.Event.ID
			h.Events[item.Event.ID] = *item.Event
		}
	}
}

func (h *historyMaterialNeed) existingContent(turns []map[string]string) {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			if obs, ok := x["media_observations"].([]any); ok {
				for _, item := range obs {
					m, _ := item.(map[string]any)
					source, _ := m["source"].(map[string]any)
					id, _ := source["event_id"].(string)
					text, _ := m["observation"].(string)
					state, _ := m["observation_state"].(string)
					if h.IDs[id] && text != "" && (state == "observed" || state == "complete" || state == "succeeded") {
						seen[id] = true
					}
				}
			}
			for _, item := range x {
				walk(item)
			}
		}
	}
	for _, turn := range turns {
		var v any
		if json.Unmarshal([]byte(turn["content"]), &v) == nil {
			walk(v)
		}
	}
	h.ReadIDs = seen
	if len(h.IDs) > 0 && len(seen) == len(h.IDs) {
		h.ContentRead = true
	}
}

// A real source-read failure may need a user-facing explanation or continuation.
// Do not turn that explanation into silence merely because content is incomplete.
func (h *historyMaterialNeed) observeReadFailure(name string, args json.RawMessage) {
	if h == nil {
		return
	}
	var q struct {
		ID      string `json:"id"`
		Message string `json:"message_id"`
		Event   bool   `json:"event"`
	}
	if json.Unmarshal(args, &q) == nil && (name == "memory_read" && (q.Event && h.IDs[q.ID] || h.MessageIDs[q.ID] != "" || h.MessageIDs[q.Message] != "") || name == "video_inspect" && h.MessageIDs[q.Message] != "") {
		h.ReadFailed = true
	}
}

func (h *historyMaterialNeed) observeRead(name string, args json.RawMessage, result string) {
	if h == nil {
		return
	}
	if h.ReadIDs == nil {
		h.ReadIDs = map[string]bool{}
	}
	var q struct {
		ID      string `json:"id"`
		Message string `json:"message_id"`
		Part    string `json:"part"`
		Action  string `json:"action"`
		Event   bool   `json:"event"`
	}
	if json.Unmarshal(args, &q) != nil {
		return
	}
	if name == "memory_read" && (q.Part == "observation" || q.Part == "transcript") {
		var p struct {
			Text  string `json:"text"`
			More  bool   `json:"has_more"`
			Event struct {
				ID string `json:"id"`
			} `json:"event"`
		}
		if json.Unmarshal([]byte(result), &p) == nil && p.Text != "" && !p.More {
			id := q.ID
			if h.IDs[p.Event.ID] {
				id = p.Event.ID
			}
			if h.IDs[id] {
				h.ReadIDs[id] = true
			}
		}
	}
	if name == "video_inspect" && (q.Action == "frames" || q.Action == "audio") {
		message := q.Message
		var p struct {
			Message string `json:"source_message_id"`
			Source  string `json:"source"`
		}
		if json.Unmarshal([]byte(result), &p) == nil {
			if h.MessageIDs[p.Message] != "" {
				message = p.Message
			} else if h.MessageIDs[p.Source] != "" {
				message = p.Source
			}
		}
		// The explicit media read is tied to one directory source. It does
		// not count as inspecting the other requested videos.
		if id := h.MessageIDs[message]; id != "" {
			h.ReadIDs[id] = true
		}
	}
	read := 0
	for id := range h.IDs {
		if h.ReadIDs[id] {
			read++
		}
	}
	h.ContentRead = len(h.IDs) > 0 && read >= len(h.IDs)
}

func (h *historyMaterialNeed) unreadDirectory() string {
	ids := []string{}
	for id := range h.IDs {
		if !h.ReadIDs[id] {
			ids = append(ids, id)
		}
	}
	sort.SliceStable(ids, func(i, j int) bool {
		a, b := h.Events[ids[i]], h.Events[ids[j]]
		if a.ID == "" || b.ID == "" {
			return ids[i] < ids[j]
		}
		return historyEventLess(a, b)
	})
	rows := []map[string]string{}
	for _, id := range ids {
		row := map[string]string{"event_id": id}
		for message, event := range h.MessageIDs {
			if event == id {
				row["message_id"] = message
				break
			}
		}
		rows = append(rows, row)
	}
	raw, _ := json.Marshal(rows)
	return string(raw)
}
