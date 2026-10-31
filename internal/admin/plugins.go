package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/plugins"
)

type pluginEntry struct {
	plugins.Installed
	Removed bool `json:"removed"`
}
type pluginLedger struct {
	mu    sync.Mutex
	path  string
	items map[string]pluginEntry
}

func newPluginLedger(dir string) (*pluginLedger, error) {
	p := &pluginLedger{path: filepath.Join(dir, "plugins.json"), items: map[string]pluginEntry{}}
	b, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil || len(b) > 2<<20 || json.Unmarshal(b, &p.items) != nil || p.items == nil || len(p.items) > 30 {
		return nil, errors.New("插件配置不可读，未自动覆盖")
	}
	if _, err := plugins.New(p.installed()); err != nil {
		return nil, errors.New("插件声明无效")
	}
	return p, nil
}
func (p *pluginLedger) installed() []plugins.Installed {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []plugins.Installed{}
	for _, item := range p.items {
		if !item.Removed {
			out = append(out, item.Installed)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out
}
func (p *pluginLedger) get(id string) (pluginEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item, ok := p.items[id]
	return item, ok
}
func (p *pluginLedger) save(item pluginEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := item.Manifest.ID
	old, exists := p.items[id]
	if !exists && len(p.items) >= 30 {
		return errors.New("最多保存 30 个插件")
	}
	p.items[id] = item
	rollback := func() {
		if exists {
			p.items[id] = old
		} else {
			delete(p.items, id)
		}
	}
	b, err := json.Marshal(p.items)
	if err != nil || len(b) > 2<<20 {
		rollback()
		return errors.New("插件配置过大")
	}
	if err = writePrivate(p.path, b); err != nil {
		rollback()
		return err
	}
	return nil
}
func (s *Server) pluginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plugins", s.protected(func(w http.ResponseWriter, r *http.Request) {
		items := []map[string]any{}
		for _, entry := range s.manager.plugins.installed() {
			keys := []string{}
			for key := range entry.Secrets {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			items = append(items, map[string]any{"id": entry.Manifest.ID, "manifest": entry.Manifest, "enabled": entry.Enabled, "secret_keys": keys})
		}
		removed := []map[string]string{}
		s.manager.plugins.mu.Lock()
		for _, entry := range s.manager.plugins.items {
			if entry.Removed {
				removed = append(removed, map[string]string{"id": entry.Manifest.ID, "name": entry.Manifest.Name})
			}
		}
		s.manager.plugins.mu.Unlock()
		sort.Slice(removed, func(i, j int) bool { return removed[i]["id"] < removed[j]["id"] })
		respond(w, 200, map[string]any{"installed": items, "removed": removed, "templates": plugins.Templates()})
	}))
	mux.HandleFunc("POST /api/plugins/install", s.protected(func(w http.ResponseWriter, r *http.Request) { s.savePlugin(w, r, true) }))
	mux.HandleFunc("POST /api/plugins/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) { s.savePlugin(w, r, false) }))
	mux.HandleFunc("POST /api/plugins/{id}/remove", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input struct{}
		if !decode(w, r, &input) {
			return
		}
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		old, ok := m.plugins.get(r.PathValue("id"))
		if !ok {
			fail(w, 404, "插件不存在")
			return
		}
		c := m.runtimeConfig()
		restart := c.Enabled && c.Companion.Enabled
		if restart {
			if err := m.stop(); err != nil {
				fail(w, 409, err.Error())
				return
			}
			defer m.start(c)
		}
		old.Removed = true
		old.Enabled = false
		if err := m.plugins.save(old); err != nil {
			fail(w, 500, "插件移除配置保存失败")
			return
		}
		m.log("plugin_disabled_and_removed")
		respond(w, 200, map[string]bool{"ok": true})
	}))
}
func (s *Server) savePlugin(w http.ResponseWriter, r *http.Request, install bool) {
	var input struct {
		Manifest     *plugins.Manifest `json:"manifest"`
		Enabled      bool              `json:"enabled"`
		Secrets      map[string]string `json:"secrets"`
		ClearSecrets bool              `json:"clear_secrets"`
	}
	if !decode(w, r, &input) {
		return
	}
	m := s.manager
	m.op.Lock()
	defer m.op.Unlock()
	var entry pluginEntry
	if install {
		if input.Manifest == nil {
			fail(w, 400, "缺少插件声明")
			return
		}
		if old, ok := m.plugins.get(input.Manifest.ID); ok && !old.Removed {
			fail(w, 409, "插件已存在，请从列表编辑")
			return
		}
		entry.Manifest = *input.Manifest
	} else {
		var ok bool
		entry, ok = m.plugins.get(r.PathValue("id"))
		if !ok {
			fail(w, 404, "插件不存在")
			return
		}
		if input.Manifest != nil {
			if input.Manifest.ID != entry.Manifest.ID {
				fail(w, 400, "修改时不能更换插件 ID")
				return
			}
			oldB, _ := json.Marshal(entry.Manifest)
			newB, _ := json.Marshal(input.Manifest)
			if string(oldB) != string(newB) && len(entry.Secrets) > 0 && len(input.Secrets) == 0 && !input.ClearSecrets {
				fail(w, 400, "修改插件声明时，请重新填写服务商密钥，避免旧密钥用于新的接口")
				return
			}
			entry.Manifest = *input.Manifest
		}
	}
	if input.ClearSecrets {
		entry.Secrets = map[string]string{}
	} else if len(input.Secrets) > 0 {
		entry.Secrets = input.Secrets
	}
	if len(entry.Secrets) > 32 {
		fail(w, 400, "密钥数量超过限制")
		return
	}
	for key, value := range entry.Secrets {
		if len(key) > 128 || len(value) > 8192 {
			fail(w, 400, "密钥字段过长")
			return
		}
	}
	if err := plugins.Validate(entry.Manifest); err != nil {
		fail(w, 400, "插件声明无效，请检查工具 schema、地址、方法与媒体配置")
		return
	}
	entry.Enabled = input.Enabled && !install
	entry.Removed = false
	all := []plugins.Installed{}
	for _, item := range m.plugins.installed() {
		if item.Manifest.ID != entry.Manifest.ID {
			all = append(all, item)
		}
	}
	all = append(all, entry.Installed)
	runtime, err := plugins.New(all)
	if err != nil {
		fail(w, 400, "插件配置无效，请检查必需密钥、工具冲突和数量上限")
		return
	}
	if entry.Enabled {
		servers, serverErr := runtime.MCPServers()
		if serverErr != nil {
			fail(w, 400, "MCP 插件密钥或服务配置不完整")
			return
		}
		m.mu.Lock()
		configured := cloneMCP(m.mcp)
		m.mu.Unlock()
		if _, err = mcptools.New(append(configured.Servers, servers...)); err != nil {
			fail(w, 400, "插件 MCP 与现有服务冲突或配置无效")
			return
		}
	}
	c := m.runtimeConfig()
	restart := c.Enabled && c.Companion.Enabled
	if restart {
		if err = m.stop(); err != nil {
			fail(w, 409, err.Error())
			return
		}
		defer m.start(c)
	}
	if err = m.plugins.save(entry); err != nil {
		fail(w, 500, "插件配置保存失败")
		return
	}
	m.log("plugin_configuration_saved")
	respond(w, 200, map[string]any{"id": entry.Manifest.ID, "enabled": entry.Enabled, "saved_at": time.Now().UTC().Format(time.RFC3339)})
}
