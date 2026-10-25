package admin

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"wechat-bot/internal/bot"
)

type surfSettings struct {
	Discovery          *discoverySettings `json:"discovery"`
	Enabled            bool               `json:"enabled"`
	ForceShare         bool               `json:"force_share"`
	IntervalMinutes    int                `json:"interval_minutes"`
	IntervalMaxMinutes int                `json:"interval_max_minutes"`
	MaxSessionsPerDay  int                `json:"max_sessions_per_day"`
	Topics             []string           `json:"topics"`
	Threads            []string           `json:"threads"`
}
type surfState struct {
	Settings   surfSettings `json:"settings"`
	Next       time.Time    `json:"next"`
	Day        string       `json:"day"`
	Sessions   int          `json:"sessions"`
	TopicIndex int          `json:"topic_index"`
}

func surfDefaults() surfState {
	return surfState{Settings: surfSettings{Discovery: defaultDiscovery(), IntervalMinutes: 60, IntervalMaxMinutes: 150, MaxSessionsPerDay: 4, Topics: []string{"可爱的动物与海洋趣闻", "轻松好听的音乐", "有趣的动画或生活短视频", "好吃的和日常小发现"}, Threads: []string{}}}
}
func loadSurf(dir string) (surfState, error) {
	s := surfDefaults()
	b, e := os.ReadFile(filepath.Join(dir, "surf.json"))
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if e != nil || len(b) > 64<<10 || json.Unmarshal(b, &s) != nil {
		return s, errors.New("网上冲浪配置不可读")
	}
	if s.Settings.Discovery == nil {
		s.Settings.Discovery = defaultDiscovery()
	}
	var fields struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	_ = json.Unmarshal(b, &fields)
	if _, exists := fields.Settings["interval_max_minutes"]; !exists {
		s.Settings.IntervalMaxMinutes = 0
	}
	if s.Settings.IntervalMaxMinutes == 0 {
		s.Settings.IntervalMaxMinutes = min(1440, s.Settings.IntervalMinutes*2)
	}
	if validateSurf(s.Settings) != nil {
		return s, errors.New("网上冲浪配置不可读")
	}
	return s, nil
}
func validateSurf(s surfSettings) error {
	if err := validateDiscovery(s.Discovery); err != nil {
		return err
	}
	if s.IntervalMinutes < 15 || s.IntervalMinutes > 1440 || s.IntervalMaxMinutes != 0 && (s.IntervalMaxMinutes < s.IntervalMinutes || s.IntervalMaxMinutes > 1440) || s.MaxSessionsPerDay < 1 || s.MaxSessionsPerDay > 24 || len(s.Topics) < 1 || len(s.Topics) > 20 || len(s.Threads) > 30 {
		return errors.New("请检查冲浪间隔、预算、兴趣与分享目标")
	}
	for _, t := range s.Topics {
		if strings.TrimSpace(t) == "" || len(t) > 200 {
			return errors.New("每个冲浪兴趣需要 1–200 字节")
		}
	}
	return nil
}
func (m *Manager) claimSurf(now time.Time) (*bot.SurfTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.surf
	if !s.Settings.Enabled || !m.config.Companion.Enabled || !m.mcp.Enabled || !m.mcp.WebFetchEnabled || m.config.Companion.Quiet(now) || now.Before(s.Next) {
		return nil, nil
	}
	loc, e := time.LoadLocation(m.config.Companion.Timezone)
	if e != nil {
		return nil, e
	}
	day := now.In(loc).Format("2006-01-02")
	if s.Day != day {
		s.Day = day
		s.Sessions = 0
	}
	if s.Sessions >= s.Settings.MaxSessionsPerDay {
		return nil, nil
	}
	topicChoice, e := rand.Int(rand.Reader, big.NewInt(int64(len(s.Settings.Topics))))
	if e != nil {
		return nil, e
	}
	topic := s.Settings.Topics[topicChoice.Int64()]
	s.TopicIndex++
	s.Sessions++
	delay, e := surfRandomDelay(s.Settings)
	if e != nil {
		return nil, e
	}
	s.Next = now.Add(delay)
	formats := []string{"text"}
	p := m.config.Companion.Permissions
	if p.SendVideo {
		formats = append(formats, "video")
	}
	if p.SendVoice {
		formats = append(formats, "voice")
	}
	if p.SendImage {
		formats = append(formats, "image")
	}
	choice, e := rand.Int(rand.Reader, big.NewInt(int64(len(formats))))
	if e != nil {
		return nil, e
	}
	preferred := formats[choice.Int64()]
	thread := ""
	if m.config.Companion.Permissions.Proactive && m.config.Companion.Permissions.ReplyText && len(s.Settings.Threads) > 0 {
		candidate := s.Settings.Threads[(s.TopicIndex-1)%len(s.Settings.Threads)]
		if permittedRecipient(m.config, candidate) {
			thread = candidate
		}
	}
	b, _ := json.Marshal(s)
	if e = writePrivate(filepath.Join(m.dir, "surf.json"), b); e != nil {
		return nil, e
	}
	m.surf = s
	return &bot.SurfTask{Topic: topic, Thread: thread, PreferredKind: preferred, ForceShare: s.Settings.ForceShare, QuietDeadline: m.config.Companion.QuietDeadline(now)}, nil
}

func (m *Manager) surfAllowed(thread string) bool {
	return m.surfAllowedAt(thread, time.Now())
}
func (m *Manager) surfAllowedAt(thread string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.surf.Settings.Enabled || !m.config.Companion.Enabled || m.config.Companion.Quiet(now) || !m.config.Companion.Permissions.Proactive || !m.config.Companion.Permissions.ReplyText || !permittedRecipient(m.config, thread) {
		return false
	}
	for _, id := range m.surf.Settings.Threads {
		if id == thread {
			return true
		}
	}
	return false
}
func (s *Server) surfRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/surf/history", s.protected(func(w http.ResponseWriter, r *http.Request) { respond(w, 200, s.manager.surfHistory.snapshot()) }))
	mux.HandleFunc("GET /api/surf", s.protected(func(w http.ResponseWriter, r *http.Request) {
		m := s.manager
		m.mu.Lock()
		state := m.surf
		config := cloneConfig(m.config)
		m.mu.Unlock()
		now := time.Now()
		sessions := state.Sessions
		if loc, err := time.LoadLocation(config.Companion.Timezone); err == nil && state.Day != now.In(loc).Format("2006-01-02") {
			sessions = 0
		}
		items := []LogEntry{}
		for _, item := range m.Logs() {
			if item.Event == "web_activity" || item.Origin == "surf" {
				items = append(items, item)
			}
		}
		respond(w, 200, map[string]any{"settings": state.Settings, "next": state.Next, "sessions": sessions, "day": state.Day, "items": items, "share_permission": config.Companion.Permissions.Proactive && config.Companion.Permissions.ReplyText, "allowed_threads": config.allowed(), "quiet_now": config.Companion.Quiet(now), "quiet_start": config.Companion.QuietStart, "quiet_end": config.Companion.QuietEnd, "timezone": config.Companion.Timezone})
	}))
	mux.HandleFunc("POST /api/surf", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input surfSettings
		if !decode(w, r, &input) {
			return
		}
		if input.Discovery == nil {
			s.manager.mu.Lock()
			input.Discovery = s.manager.surf.Settings.Discovery
			s.manager.mu.Unlock()
			if input.Discovery == nil {
				input.Discovery = defaultDiscovery()
			}
		}
		if input.IntervalMaxMinutes == 0 {
			input.IntervalMaxMinutes = min(1440, input.IntervalMinutes*2)
		}
		if e := validateSurf(input); e != nil {
			fail(w, 400, e.Error())
			return
		}
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		seen := map[string]bool{}
		for _, id := range input.Threads {
			if !permittedRecipient(m.config, id) || seen[id] {
				fail(w, 400, "分享目标必须是已授权且不重复的会话")
				return
			}
			seen[id] = true
		}
		state := m.surf
		state.Settings = input
		delay, err := surfRandomDelay(input)
		if err != nil {
			fail(w, 500, "随机调度暂时不可用")
			return
		}
		state.Next = time.Now().Add(delay)
		b, _ := json.Marshal(state)
		if e := writePrivate(filepath.Join(m.dir, "surf.json"), b); e != nil {
			fail(w, 500, e.Error())
			return
		}
		m.surf = state
		respond(w, 200, state.Settings)
	}))
}
func surfRandomDelay(s surfSettings) (time.Duration, error) {
	maxMinutes := s.IntervalMaxMinutes
	if maxMinutes == 0 {
		maxMinutes = min(1440, s.IntervalMinutes*2)
	}
	minimum := time.Duration(s.IntervalMinutes) * time.Minute
	span := time.Duration(maxMinutes-s.IntervalMinutes) * time.Minute
	if span <= 0 {
		return minimum, nil
	}
	n, e := rand.Int(rand.Reader, big.NewInt(int64(span/time.Second)+1))
	if e != nil {
		return 0, e
	}
	return minimum + time.Duration(n.Int64())*time.Second, nil
}
