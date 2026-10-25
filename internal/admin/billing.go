package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"wechat-bot/internal/deepseek"
)

type tokenTotals struct {
	Requests int64              `json:"requests"`
	Reported int64              `json:"reported_requests"`
	Input    int64              `json:"input_tokens"`
	Output   int64              `json:"output_tokens"`
	CacheHit int64              `json:"cache_hit_tokens"`
	Unpriced int64              `json:"unpriced_requests"`
	Costs    map[string]float64 `json:"estimated_costs"`
}
type modelUsage struct {
	ID       string      `json:"id"`
	Provider string      `json:"provider"`
	Model    string      `json:"model"`
	Total    tokenTotals `json:"total"`
}
type tokenPrices struct {
	Currency string  `json:"currency"`
	Input    float64 `json:"input_per_million"`
	CacheHit float64 `json:"cache_hit_per_million"`
	Output   float64 `json:"output_per_million"`
}
type usageData struct {
	Version int                    `json:"version"`
	Started time.Time              `json:"started_at"`
	Total   tokenTotals            `json:"total"`
	Days    map[string]tokenTotals `json:"days"`
	Models  map[string]modelUsage  `json:"models"`
	Prices  map[string]tokenPrices `json:"prices"`
}
type billingStore struct {
	mu        sync.Mutex
	path      string
	data      usageData
	balanceMu sync.Mutex
	balanceID string
	balanceAt time.Time
	balance   balanceView
}
type balanceView struct {
	State   string            `json:"state"`
	Checked time.Time         `json:"checked_at,omitempty"`
	Reason  string            `json:"reason,omitempty"`
	Data    *deepseek.Balance `json:"data,omitempty"`
}

func newBillingStore(dir string) (*billingStore, error) {
	s := &billingStore{path: filepath.Join(dir, "usage.json"), data: usageData{Version: 1, Started: time.Now().UTC(), Days: map[string]tokenTotals{}, Models: map[string]modelUsage{}, Prices: map[string]tokenPrices{}}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		initial, _ := json.Marshal(s.data)
		if err := writePrivate(s.path, initial); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil || len(b) > 2<<20 || json.Unmarshal(b, &s.data) != nil || s.data.Version != 1 || s.data.Days == nil || s.data.Models == nil || s.data.Prices == nil || len(s.data.Days) > 370 || len(s.data.Models) > 128 || len(s.data.Prices) > 128 {
		return nil, errors.New("用量账本不可读，未自动重置")
	}
	return s, nil
}
func billingIdentity(c Config) string {
	sum := sha256.Sum256([]byte(c.BaseURL + "\x00" + c.APIKey + "\x00" + c.Model))
	return hex.EncodeToString(sum[:])[:20]
}
func officialBilling(c Config) bool {
	u, e := url.Parse(c.BaseURL)
	return e == nil && c.Provider == "deepseek" && u.Scheme == "https" && u.Host == "api.deepseek.com" && (u.Path == "" || u.Path == "/" || u.Path == "/v1" || u.Path == "/v1/")
}
func referencePrices(c Config, stamp time.Time) (tokenPrices, bool) {
	if !officialBilling(c) {
		return tokenPrices{}, false
	}
	p := tokenPrices{Currency: "USD"}
	switch c.Model {
	case "deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp":
		p.Input = .15
		p.CacheHit = .003
		p.Output = .6
	case "deepseek-v4-pro":
		p.Input = .66
		p.CacheHit = .022
		p.Output = 1.98
	default:
		return tokenPrices{}, false
	}
	u := stamp.UTC()
	hour := u.Hour()
	if u.Weekday() != time.Saturday && u.Weekday() != time.Sunday && ((hour >= 1 && hour < 4) || (hour >= 6 && hour < 10)) {
		p.Input *= 2
		p.CacheHit *= 2
		p.Output *= 2
	}
	return p, true
}
func (s *billingStore) pricesLocked(c Config, stamp time.Time) (tokenPrices, bool, bool) {
	if p, ok := s.data.Prices[billingIdentity(c)]; ok {
		return p, true, true
	}
	p, ok := referencePrices(c, stamp)
	return p, ok, false
}
func addUsage(t tokenTotals, u deepseek.Usage, p tokenPrices, priced bool) tokenTotals {
	t.Requests++
	if u.Reported {
		t.Reported++
		t.Input += u.Input
		t.Output += u.Output
		t.CacheHit += u.CacheHit
		if priced {
			if t.Costs == nil {
				t.Costs = map[string]float64{}
			}
			t.Costs[p.Currency] += (float64(u.Input-u.CacheHit)*p.Input + float64(u.CacheHit)*p.CacheHit + float64(u.Output)*p.Output) / 1_000_000
		} else {
			t.Unpriced++
		}
	}
	return t
}
func (s *billingStore) record(c Config, u deepseek.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prior, _ := json.Marshal(s.data)
	id := billingIdentity(c)
	entry, exists := s.data.Models[id]
	if !exists && len(s.data.Models) >= 128 {
		return errors.New("用量模型上限")
	}
	uurl, _ := url.Parse(c.BaseURL)
	label := c.Provider
	if uurl != nil && uurl.Hostname() != "" {
		label = uurl.Hostname()
	}
	entry.ID = id
	entry.Provider = label
	entry.Model = c.Model
	p, ok, _ := s.pricesLocked(c, u.Time)
	s.data.Total = addUsage(s.data.Total, u, p, ok)
	location, _ := time.LoadLocation("Asia/Shanghai")
	day := u.Time.In(location).Format("2006-01-02")
	s.data.Days[day] = addUsage(s.data.Days[day], u, p, ok)
	entry.Total = addUsage(entry.Total, u, p, ok)
	s.data.Models[id] = entry
	for old := range s.data.Days {
		if old < u.Time.AddDate(0, 0, -365).In(location).Format("2006-01-02") {
			delete(s.data.Days, old)
		}
	}
	b, err := json.Marshal(s.data)
	if err == nil {
		err = writePrivate(s.path, b)
	}
	if err != nil {
		_ = json.Unmarshal(prior, &s.data)
	}
	return err
}
func (s *billingStore) snapshot(c Config) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Deep copy maps before releasing the lock and serializing in HTTP handlers.
	b, _ := json.Marshal(s.data)
	var d usageData
	_ = json.Unmarshal(b, &d)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	day := time.Now().In(loc).Format("2006-01-02")
	models := []modelUsage{}
	for _, m := range d.Models {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	p, known, custom := s.pricesLocked(c, time.Now())
	return map[string]any{"started_at": d.Started, "today": d.Days[day], "total": d.Total, "days": d.Days, "models": models, "active_id": billingIdentity(c), "active_model": c.Model, "prices": p, "priced": known, "custom_prices": custom, "price_reference_date": "2026-10-25", "price_source": "https://api-docs.deepseek.com/quick_start/pricing/", "cost_is_estimate": true, "timezone": "Asia/Shanghai"}
}
func validPrices(p tokenPrices) bool {
	if p.Currency != "USD" && p.Currency != "CNY" {
		return false
	}
	for _, v := range []float64{p.Input, p.CacheHit, p.Output} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1_000_000 {
			return false
		}
	}
	return true
}
func (s *Server) billingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/billing", s.protected(func(w http.ResponseWriter, r *http.Request) {
		m := s.manager
		m.mu.Lock()
		c := cloneConfig(m.config)
		m.mu.Unlock()
		respond(w, 200, m.billing.snapshot(c))
	}))
	mux.HandleFunc("POST /api/billing/prices", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ActiveID string `json:"active_id"`
			Reset    bool   `json:"reset"`
			tokenPrices
		}
		if !decode(w, r, &in) {
			return
		}
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		m.mu.Lock()
		c := cloneConfig(m.config)
		m.mu.Unlock()
		if in.ActiveID != billingIdentity(c) {
			fail(w, 409, "服务商或模型已切换，请刷新单价再保存")
			return
		}
		if !in.Reset && !validPrices(in.tokenPrices) {
			fail(w, 400, "请输入有效币种及每百万Token单价")
			return
		}
		b := m.billing
		b.mu.Lock()
		before, _ := json.Marshal(b.data)
		if in.Reset {
			delete(b.data.Prices, in.ActiveID)
		} else {
			if len(b.data.Prices) >= 128 {
				b.mu.Unlock()
				fail(w, 400, "单价设置达到上限")
				return
			}
			b.data.Prices[in.ActiveID] = in.tokenPrices
		}
		out, _ := json.Marshal(b.data)
		err := writePrivate(b.path, out)
		if err != nil {
			_ = json.Unmarshal(before, &b.data)
		}
		b.mu.Unlock()
		if err != nil {
			fail(w, 500, "单价保存失败")
			return
		}
		respond(w, 200, b.snapshot(c))
	}))
	mux.HandleFunc("POST /api/billing/balance", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct{}
		if !decode(w, r, &in) {
			return
		}
		m := s.manager
		m.mu.Lock()
		c := cloneConfig(m.config)
		m.mu.Unlock()
		b := m.billing
		if !officialBilling(c) {
			respond(w, 200, balanceView{State: "unsupported", Reason: "当前兼容服务商未提供统一余额接口"})
			return
		}
		if c.APIKey == "" {
			respond(w, 200, balanceView{State: "unconfigured", Reason: "请先保存服务商Key"})
			return
		}
		b.balanceMu.Lock()
		defer b.balanceMu.Unlock()
		id := billingIdentity(c)
		if b.balanceID == id && time.Since(b.balanceAt) < 30*time.Second {
			respond(w, 200, b.balance)
			return
		}
		provider, err := m.makeModel(c)
		view := balanceView{State: "error", Checked: time.Now().UTC(), Reason: "余额查询暂不可用"}
		if err == nil {
			var balance deepseek.Balance
			balance, err = provider.Balance(r.Context())
			if err == nil {
				view.State = "ok"
				view.Reason = ""
				view.Data = &balance
			} else {
				view.Reason = balanceReason(err)
			}
		}
		b.balanceID = id
		b.balanceAt = time.Now()
		b.balance = view
		respond(w, 200, view)
	}))
}
func balanceReason(err error) string {
	switch err.Error() {
	case "balance_http_401", "balance_http_403":
		return "余额查询鉴权失败，请检查Key"
	case "balance_http_429":
		return "余额查询被限流，请稍后刷新"
	case "balance_unsupported":
		return "当前地址不支持官方余额查询"
	case "balance_network_failed":
		return "余额查询连接失败，请稍后刷新"
	default:
		return "余额服务返回异常，未显示为零余额"
	}
}
