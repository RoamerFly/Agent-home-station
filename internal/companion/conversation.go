package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// ConversationSettings controls scheduling, never extra recipient/tool permissions.
type ConversationSettings struct {
	Enabled          bool `json:"enabled"`
	AggregateMS      int  `json:"aggregate_ms"`
	RecheckOnMessage bool `json:"recheck_on_message"`
	AllowWake        bool `json:"allow_wake"`
	WakeMinSeconds   int  `json:"wake_min_seconds"`
	WakeMaxSeconds   int  `json:"wake_max_seconds"`
}

func ConversationDefaults() *ConversationSettings {
	return &ConversationSettings{Enabled: true, AggregateMS: 2000, RecheckOnMessage: true, AllowWake: false, WakeMinSeconds: 30, WakeMaxSeconds: 600}
}
func (c Config) ConversationConfig() ConversationSettings {
	if c.Conversation == nil {
		return *ConversationDefaults()
	}
	return *c.Conversation
}
func validateConversation(c *ConversationSettings) error {
	if c == nil {
		return nil
	}
	if c.AggregateMS < 0 || c.AggregateMS > 5000 || c.WakeMinSeconds < 15 || c.WakeMaxSeconds < c.WakeMinSeconds || c.WakeMaxSeconds > 3600 {
		return errors.New("请检查消息聚合窗口与延时补充范围")
	}
	return nil
}

type PendingReview struct {
	EvidenceMessageID string   `json:"evidence_message_id,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	Action            string   `json:"action"`
	Messages          []string `json:"messages,omitempty"`
	DelaySeconds      int      `json:"delay_seconds,omitempty"`
}

func (s *Store) ClaimConversationWake(in Input, c Config, now time.Time) error {
	return s.transaction(func(data *persisted) error {
		if !c.Enabled || !c.ConversationConfig().AllowWake || !c.Permissions.Proactive || !c.Permissions.ReplyText || !c.Permissions.Remember || c.Quiet(now) {
			return errors.New("conversation wake permission denied")
		}
		selected := false
		for _, id := range c.ProactiveThreads {
			selected = selected || id == in.Thread
		}
		if !selected {
			return errors.New("conversation wake target not selected")
		}
		id := ScopeID(in.Channel, in.Account, in.Thread)
		scope, ok := data.Scopes[id]
		if !ok || len(scope.Messages) == 0 || now.Sub(scope.LastInteraction) > time.Duration(c.MemoryDays)*24*time.Hour {
			return errors.New("conversation wake context unavailable")
		}
		day := localDay(c, now)
		if data.ProactiveDay != day {
			data.ProactiveDay = day
			data.ProactiveCount = 0
		}
		if data.ProactiveCount >= c.ProactiveMaxPerDay || proactiveToday(scope, c, now) >= c.ProactiveRule(in.Thread).MaxPerDay {
			return errors.New("conversation wake daily budget exhausted")
		}
		scope.ProactiveCount = proactiveToday(scope, c, now) + 1
		scope.ProactiveDay = day
		scope.LastProactive = now
		data.Scopes[id] = scope
		data.ProactiveCount++
		return nil
	})
}

// ReviewPending cannot execute tools or send. Only the unsent tail can change.
func (e *Engine) ReviewPending(ctx context.Context, in Input, sent, pending []string, updates []ConversationEvent) (PendingReview, error) {
	return e.reviewPending(ctx, in, sent, pending, updates, nil, false)
}

// Running requests are not the unsent tail of an obsolete text reply.
func (e *Engine) ReviewGeneration(ctx context.Context, in Input, pending []string, updates []ConversationEvent) (PendingReview, error) {
	return e.reviewPending(ctx, in, nil, pending, updates, nil, true)
}

// DeliveryReview carries actual delivery evidence. This call cannot use tools.
type DeliveryReview struct {
	Reason   string    `json:"reason"`
	Attempts int       `json:"retry_attempts"`
	Created  time.Time `json:"first_attempt_at"`
}

func (e *Engine) ReviewDelivery(ctx context.Context, in Input, sent []string, text string, delivery DeliveryReview) (PendingReview, error) {
	return e.reviewPending(ctx, in, sent, []string{text}, nil, &delivery, false)
}

func (e *Engine) reviewPending(ctx context.Context, in Input, sent, pending []string, updates []ConversationEvent, delivery *DeliveryReview, generation bool) (PendingReview, error) {
	data := map[string]any{"kind": "review_unsent_message_plan", "original_request": in.Text, "original_requester_id": in.Sender, "already_sent": sent, "pending_messages": pending, "new_events": updates, "current_time": time.Now().In(location(e.Config.Timezone)).Format(time.RFC3339)}
	if generation {
		data["kind"] = "review_running_task"
	}
	if delivery != nil {
		data["kind"] = "review_uncertain_delivery"
		data["delivery_evidence"] = delivery
	}
	scope := e.Store.Context(in, e.Config, time.Now())
	data["relationship_notes"] = scope.Notes
	history := scope.Messages
	if len(history) > 10 {
		history = history[len(history)-10:]
	}
	data["recent_conversation"] = history
	encoded, _ := json.Marshal(data)
	system := e.policy(in) + "\n本轮只复核旧发言计划的未发部分，不是回复最新用户的独立任务。新请求由另一轮处理，别抢答或重复生成其答案。只输出JSON {action:continue|cancel|replace|delay,messages:[语义完整的未发短句],delay_seconds:整数}。continue保留，cancel取消旧尾部，replace只修改有必要保留的旧补充，delay暂缓。根据新上下文判断；无关群友互聊不必打断。不能改已经发送的内容，不调用工具、不生成媒体、不承诺执行。取消需识别具体哪条新消息使原请求失效；原请求者真正叫停或改需求时才取消执行任务。其他人说机器人未开机、笑脸、生日祝福、聊别的话题、催促或重复原请求，都不代表撤销；不因最新话题变化就丢弃尚未完成的唱歌/下载请求。图片、视频、语音及分享的观察内容属于原请求的一部分；同一人紧跟发来的短说明、名称、图注通常是在补充媒体，要结合实际观察继续理解，不能仅因后发文字被标记为new_request就当成替代。只有明确撤销或相冲突的新要求才取消媒体任务；读取完成后仍可按正常社交判断选择安静。无法确定时continue。旧内容已过时、用户改意或明确叫停时取消。不要机械按标点拆分，不固定气泡数。"
	if delivery != nil {
		system += "\n本轮是发送回执不确定后的补发复核。你已知原文、补发次数和已确认成功内容。回执不确定不等于未送达，不能假装收到或宣称网络故障已修好。结合最近对话决定自然地沿用或改写这条补充，不机械重复、无需每次道歉；不要再次发送已接受的语音或文件。不因单次失败就放弃仍有效的请求。只有明确撤销、确认重复或确已过时才cancel，并给reason。replace只返回一条短消息。无法判断则delay，等待稍后核对。"
	}
	if in.IsGroup {
		system += "\n群里补发应像正常聊天，不能添加本系统的发送错误、回执、重试次数、任务编号或修复进度等运维说明；这些只私聊主人。若待发内容本身是纯运维说明，可cancel这条说明并解释改走私聊，不代表取消原用户任务或附件补发。"
	}
	if generation {
		system = e.policy(in) + "\n本轮检查正在处理的原始请求是否被真正撤销，不是修改旧回复或取消发言尾部。original_request包含原入站内容和已经取得的真实媒体观察；new_events是处理期间的后续消息。只输出JSON {action:continue|cancel|delay,evidence_message_id:字符串,reason:简短依据,delay_seconds:整数}，不调用工具、不回答用户。图片/视频之后同一人发短说明、名称或图注，是补充同一条媒体，应continue，让原主任务结合这些资料完成理解并决定回复或安静。不要因为后续消息标记new_request、另有一轮会处理文字、缺少明确提问或群话题变化就cancel。阅读完后是否接话由主决策的reply/silent决定，不能拿cancel代替安静。只有原请求者明确叫停，或明确提出与原任务相冲突的替代要求，才cancel；必须给真实依据消息ID和具体撤销含义。催促、补充说明、其他人聊天均不撤销原请求。不能撤回已发生的外部操作。无法确认撤销时continue。"
	}
	answer, err := e.model.Chat(ctx, system, []map[string]string{{"role": "user", "content": string(encoded)}})
	if err != nil {
		return PendingReview{}, err
	}
	var d PendingReview
	if len(answer) > 12<<10 || json.Unmarshal([]byte(strings.TrimSpace(answer)), &d) != nil {
		return d, errors.New("invalid pending-plan review")
	}
	switch d.Action {
	case "continue", "cancel", "delay":
	case "replace":
		if generation {
			return d, errors.New("running task review cannot replace a reply plan")
		}
		if len(d.Messages) < 1 || len(d.Messages) > e.Config.MaxBubbles {
			return d, errors.New("invalid replacement plan")
		}
	default:
		return d, errors.New("invalid pending-plan action")
	}
	for _, s := range d.Messages {
		if strings.TrimSpace(s) == "" || len(s) > 3000 || !utf8.ValidString(s) {
			return d, errors.New("invalid pending-plan message")
		}
	}
	if d.DelaySeconds < 0 || d.DelaySeconds > 300 {
		return d, errors.New("invalid pending-plan delay")
	}
	return d, nil
}

type ConversationEvent struct {
	MessageID  string `json:"message_id"`
	Sender     string `json:"sender"`
	Text       string `json:"text"`
	At         string `json:"at"`
	NewRequest bool   `json:"new_request"`
}
