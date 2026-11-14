package station

// homeCatalog is the smallest possible index of the home: application names and
// their on/off state, with no conversation text and no memory. It exists so the
// companion knows which applications live here without loading any of their
// content, and therefore knows when a question needs an on-demand lookup.
//
// The catalog is deliberately built from the visible registry only: an
// application the owner has detached is not part of the home and must not be
// announced as one.
func (h *localChatService) homeCatalog() map[string]any {
	if h == nil {
		return nil
	}
	apps := h.applications.Applications()
	if len(apps) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(apps))
	for _, app := range apps {
		item := map[string]any{"id": app.ID, "name": app.Name}
		if hint := homeAppHint(app.ID); hint != "" {
			item["hint"] = hint
		}
		if h.wechat != nil && app.ID == "wechat" {
			power := h.wechat.appPower()
			item["state"] = power.state()
			item["connections"] = power.Connections
		}
		items = append(items, item)
	}
	return map[string]any{
		"kind":  "home_apps",
		"count": len(items),
		"items": items,
		"note": "这里只有应用名字和开关状态，不含任何聊天内容或记忆。" +
			"要回答某个应用里的具体情况，用 app_memory 传 app_id，先看 sources 再检索与当前问题相关的片段；" +
			"state=off 的应用此刻不在家，不读取它的记忆，也不替它发言。" +
			"没有列出的应用不存在，不要假设。",
	}
}

// homeHasApp reports whether the application is part of this home at all,
// whatever its switch state. The catalog announces such an application, so its
// memory lookup has to stay reachable even when the owner never types its name:
// "短视频那边" is a question about the application, not about a keyword.
func (h *localChatService) homeHasApp(id string) bool {
	if h == nil {
		return false
	}
	_, ok := h.applications.Lookup(id)
	return ok
}

// homeAppHint is one short phrase per known application. Unknown applications
// simply carry no hint instead of an invented description.
func homeAppHint(id string) string {
	switch id {
	case "wechat":
		return "微信会话与群聊"
	case "douyin":
		return "抖音作品、评论与互动通知"
	case "bilibili":
		return "哔哩哔哩动态与公开内容"
	}
	return ""
}

// state collapses the connection view into the three states the companion can
// actually act on: not connected, on, off.
func (p wechatAppPower) state() string {
	switch {
	case p.Connections == 0:
		return "not_connected"
	case p.Paused >= p.Connections:
		return "off"
	}
	return "on"
}

// off reports whether the owner switched this application off while it still
// has enabled connections. A home with no enabled connection is not "off": it
// simply has nothing connected yet.
func (p wechatAppPower) off() bool { return p.Connections > 0 && p.Paused >= p.Connections }
