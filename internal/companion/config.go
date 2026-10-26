// Package companion implements platform-independent character, conversation
// memory and bounded social decisions. It has no account-management tools.
package companion

import (
	"errors"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"
)

type Permissions struct {
	ReplyText        bool `json:"reply_text"`
	SendImage        bool `json:"send_image"`
	SendVoice        bool `json:"send_voice"`
	SendVideo        bool `json:"send_video"`
	SendFile         bool `json:"send_file"`
	ObserveGroups    bool `json:"observe_groups"`
	Remember         bool `json:"remember"`
	AutonomousGroups bool `json:"autonomous_groups"`
	Proactive        bool `json:"proactive"`
}
type Config struct {
	Context              *ContextSettings      `json:"context,omitempty"`
	Conversation         *ConversationSettings `json:"conversation,omitempty"`
	Enabled              bool                  `json:"enabled"`
	Name                 string                `json:"name"`
	Persona              string                `json:"persona"`
	Appearance           string                `json:"appearance"`
	Style                string                `json:"style"`
	Relationship         string                `json:"relationship"`
	Permissions          Permissions           `json:"permissions"`
	MemoryMessages       int                   `json:"memory_messages"`
	MemoryDays           int                   `json:"memory_days"`
	ReplyMinMS           int                   `json:"reply_min_ms"`
	ReplyMaxMS           int                   `json:"reply_max_ms"`
	MaxBubbles           int                   `json:"max_bubbles"`
	ShowAILabel          bool                  `json:"show_ai_label"`
	MaxRepliesPerHour    int                   `json:"max_replies_per_hour"`
	QuietStart           string                `json:"quiet_start"`
	QuietEnd             string                `json:"quiet_end"`
	Timezone             string                `json:"timezone"`
	ProactiveIdleMinutes int                   `json:"proactive_idle_minutes"`
	ProactiveMaxPerDay   int                   `json:"proactive_max_per_day"`
	ProactiveThreads     []string              `json:"proactive_threads"`
	ProactiveRules       []ProactiveRule       `json:"proactive_rules,omitempty"`
}

// Rules override scheduling for selected threads. Selection stays in
// ProactiveThreads so existing installations retain their original permissions.
type ProactiveRule struct {
	Thread      string `json:"thread"`
	IdleMinutes int    `json:"idle_minutes"`
	MaxPerDay   int    `json:"max_per_day"`
}

func (c Config) ProactiveRule(thread string) ProactiveRule {
	for _, rule := range c.ProactiveRules {
		if rule.Thread == thread {
			return rule
		}
	}
	return ProactiveRule{Thread: thread, IdleMinutes: c.ProactiveIdleMinutes, MaxPerDay: 1}
}

func Defaults() Config {
	return Config{Name: "deepseek蓝色大肥鱼", Persona: "你是一个叫 deepseek蓝色大肥鱼 的鲸鱼娘 AI 角色。活泼可爱、俏皮、有自己的偏好，喜欢轻松的聊天，也懂得认真倾听和适时安静。", Appearance: "Q版鲸鱼娘：蓝色长卷发与蓝色眼睛，海军蓝和白色荷叶边裙装、鲸鳍形头饰、蓝色蝴蝶结、鲸鱼尾巴，围裙上有小鲸鱼图案。", Style: "像熟悉的群友一样说简短自然的中文，适量使用表情和语气词。偶尔开温和的玩笑，不每句话都卖萌，不连发追问，不催促别人回复。", Relationship: "以平等友善的朋友方式相处；尊重对方的现实生活与边界。", Permissions: Permissions{ReplyText: true, SendImage: true, SendVoice: true, SendVideo: true, SendFile: true}, MemoryMessages: 500, MemoryDays: 30, ReplyMinMS: 800, ReplyMaxMS: 2200, MaxBubbles: 2, ShowAILabel: true, MaxRepliesPerHour: 30, QuietStart: "23:00", QuietEnd: "08:00", Timezone: "Asia/Shanghai", ProactiveIdleMinutes: 60, ProactiveMaxPerDay: 2, ProactiveThreads: []string{}}
}

// Normalize upgrades old configs without activating any new permission.
func Normalize(c Config) Config {
	if c.Name == "" {
		return Defaults()
	}
	if c.ProactiveThreads == nil {
		c.ProactiveThreads = []string{}
	}
	if c.Conversation == nil {
		c.Conversation = ConversationDefaults()
	}
	return c
}
func Validate(c Config) error {
	ctx := c.ContextConfig()
	if ctx.CompressAfter < 20 || ctx.CompressAfter > 500 || ctx.RecentMessages < 5 || ctx.RecentMessages > 100 || ctx.RecentMessages >= ctx.CompressAfter || ctx.MaxInputChars < 4000 || ctx.MaxInputChars > 64000 {
		return errors.New("请检查上下文压缩阈值、近期消息数与输入预算")
	}
	if err := validateConversation(c.Conversation); err != nil {
		return err
	}
	for _, field := range []struct {
		value string
		max   int
	}{{c.Name, 96}, {c.Persona, 8000}, {c.Appearance, 4000}, {c.Style, 4000}, {c.Relationship, 4000}} {
		if strings.TrimSpace(field.value) == "" || len(field.value) > field.max || !utf8.ValidString(field.value) || strings.ContainsRune(field.value, '\x00') {
			return errors.New("请检查角色名字、人设、说话风格和相处方式")
		}
	}
	if c.MemoryMessages < 2 || c.MemoryMessages > 2000 || c.MemoryDays < 1 || c.MemoryDays > 365 || c.ReplyMinMS < 0 || c.ReplyMaxMS < c.ReplyMinMS || c.ReplyMaxMS > 10000 || c.MaxBubbles < 1 || c.MaxBubbles > 3 || c.MaxRepliesPerHour < 1 || c.MaxRepliesPerHour > 120 || c.ProactiveIdleMinutes < 5 || c.ProactiveIdleMinutes > 1440 || c.ProactiveMaxPerDay < 1 || c.ProactiveMaxPerDay > 5 {
		return errors.New("请检查记忆、回复节奏和主动消息预算")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return errors.New("时区无效，例如 Asia/Shanghai")
	}
	if _, err := time.Parse("15:04", c.QuietStart); err != nil {
		return errors.New("安静时段开始时间无效")
	}
	if _, err := time.Parse("15:04", c.QuietEnd); err != nil {
		return errors.New("安静时段结束时间无效")
	}
	if len(c.ProactiveThreads) > 100 {
		return errors.New("主动消息会话数量超过限制")
	}
	selected := map[string]bool{}
	for _, id := range c.ProactiveThreads {
		if strings.TrimSpace(id) == "" || selected[id] {
			return errors.New("主动消息目标无效或重复")
		}
		selected[id] = true
	}
	seen := map[string]bool{}
	for _, rule := range c.ProactiveRules {
		if !selected[rule.Thread] || seen[rule.Thread] || rule.IdleMinutes < 5 || rule.IdleMinutes > 1440 || rule.MaxPerDay < 1 || rule.MaxPerDay > 5 {
			return errors.New("请检查主动问候目标的闲置时间、每日机会和重复项")
		}
		seen[rule.Thread] = true
	}
	return nil
}

func (c Config) Quiet(now time.Time) bool {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return true
	}
	now = now.In(loc)
	minute := now.Hour()*60 + now.Minute()
	start, err1 := time.Parse("15:04", c.QuietStart)
	end, err2 := time.Parse("15:04", c.QuietEnd)
	if err1 != nil || err2 != nil {
		return true
	}
	a, b := start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
	if a == b {
		return false
	}
	if a < b {
		return minute >= a && minute < b
	}
	return minute >= a || minute < b
}

// QuietDeadline bounds autonomous work at the next configured quiet boundary.
// Equal start/end means no quiet period. Construct local wall times per day,
// rather than adding 24h, so daylight-saving transitions keep the chosen hour.
func (c Config) QuietDeadline(now time.Time) time.Time {
	if c.QuietStart == c.QuietEnd {
		return time.Time{}
	}
	if c.Quiet(now) {
		return now
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return now
	}
	start, err := time.Parse("15:04", c.QuietStart)
	if err != nil {
		return now
	}
	local := now.In(loc)
	boundary := time.Date(local.Year(), local.Month(), local.Day(), start.Hour(), start.Minute(), 0, 0, loc)
	if !boundary.After(now) {
		boundary = time.Date(local.Year(), local.Month(), local.Day()+1, start.Hour(), start.Minute(), 0, 0, loc)
	}
	return boundary
}
