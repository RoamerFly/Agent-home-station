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
	Anchor      string
	Count       int
	IDs         map[string]bool
	MessageIDs  map[string]string
	ReadIDs     map[string]bool
	FailedIDs   map[string]bool
	Events      map[string]MemoryEvent
	ReadPackets map[string]string // exact tool feedback -> verified directory event
}

// HistoryReadCoverage is host-authored execution evidence for a selected
// historical range. It intentionally leaves unattempted sources out of Failed:
// selection alone never proves a read failure.
type HistoryReadCoverage struct {
	Requested      int      `json:"requested"`
	Selected       int      `json:"selected"`
	Attempted      int      `json:"attempted"`
	Read           int      `json:"read"`
	Failed         int      `json:"failed"`
	FailedEventIDs []string `json:"failed_event_ids,omitempty"`
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

// Recognize an explicit numbered previous-video request so that model-selected
// reads can be sorted and attributed. Recognition itself never fetches data.
func requestedHistory(turns []map[string]string) *historyMaterialNeed {
	for i := len(turns) - 1; i >= 0; i-- {
		var p struct {
			Kind, Text string
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
		return &historyMaterialNeed{Anchor: anchor, Count: count, IDs: map[string]bool{}, MessageIDs: map[string]string{}, ReadIDs: map[string]bool{}, FailedIDs: map[string]bool{}}
	}
	return nil
}

func (h *historyMaterialNeed) readEventID(name string, args json.RawMessage) string {
	if h == nil {
		return ""
	}
	var q struct {
		ID      string `json:"id"`
		Message string `json:"message_id"`
	}
	if json.Unmarshal(args, &q) != nil {
		return ""
	}
	if name == "memory_read" {
		if h.IDs[q.ID] {
			return q.ID
		}
		if id := h.MessageIDs[q.ID]; id != "" {
			return id
		}
		return h.MessageIDs[q.Message]
	}
	if name == "video_inspect" {
		return h.MessageIDs[q.Message]
	}
	return ""
}

// observeReadFailure records only a source that was actually selected and
// actually attempted. It is reporting metadata, never a gate on the reply.
func (h *historyMaterialNeed) observeReadFailure(name string, args json.RawMessage) {
	if id := h.readEventID(name, args); id != "" {
		if h.FailedIDs == nil {
			h.FailedIDs = map[string]bool{}
		}
		h.FailedIDs[id] = true
	}
}

func (h *historyMaterialNeed) coverage() *HistoryReadCoverage {
	if h == nil || len(h.Events) == 0 {
		return nil
	}
	coverage := &HistoryReadCoverage{Requested: h.Count, Selected: len(h.Events)}
	for id := range h.Events {
		if h.ReadIDs[id] {
			coverage.Read++
			coverage.Attempted++
			continue
		}
		if h.FailedIDs[id] {
			coverage.Failed++
			coverage.Attempted++
			coverage.FailedEventIDs = append(coverage.FailedEventIDs, id)
		}
	}
	if coverage.Attempted == 0 {
		return nil
	}
	sort.SliceStable(coverage.FailedEventIDs, func(i, j int) bool {
		return historyEventLess(h.Events[coverage.FailedEventIDs[i]], h.Events[coverage.FailedEventIDs[j]])
	})
	return coverage
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
				delete(h.FailedIDs, id)
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
			delete(h.FailedIDs, id)
		}
	}
}
