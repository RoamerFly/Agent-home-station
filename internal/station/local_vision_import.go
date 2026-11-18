package station

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"wechat-bot/internal/localasr"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/workspacefs"
)

// Read only the old source once. The workstation store is the only editable
// source after migration. Previously saved Station settings always take priority.
func (s *localProjectTools) importLegacyPublicToolConfig(ctx context.Context, host *localProjectToolHost, id string) {
	if host == nil {
		return
	}
	current, err := host.store.Configuration(ctx, id)
	if err != nil || current.Revision != 0 {
		return
	}
	dir := filepath.Join(s.home.dataDir, "applications", "wechat", "legacy-admin", "plugin-host")
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		return
	}
	defer root.Close()
	f, err := workspacefs.OpenRegular(root, "plugins.sqlite")
	if err != nil {
		return
	}
	f.Close()
	path := filepath.ToSlash(filepath.Join(dir, "plugins.sqlite"))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return
	}
	defer db.Close()
	var raw, secret []byte
	if err = db.QueryRowContext(ctx, `SELECT c.values_json,c.secrets_json FROM configurations c JOIN installed i ON i.id=c.plugin WHERE c.plugin=? AND i.removed=0`, id).Scan(&raw, &secret); err != nil {
		return
	}
	var original map[string]json.RawMessage
	var private map[string]string
	if json.Unmarshal(raw, &original) != nil || json.Unmarshal(secret, &private) != nil {
		return
	}
	var names []string
	var key string
	switch id {
	case localasr.PluginID:
		names = []string{"vision_endpoint", "vision_model", "vision_backend", "max_tokens", "thinking", "reasoning_effort"}
		key = "vision_key"
	case "web-search":
		names = []string{"search_backend"}
		key = "search_key"
	default:
		return
	}
	if private[key] == "" {
		return
	}
	values := map[string]json.RawMessage{}
	for _, name := range names {
		if value, ok := original[name]; ok {
			values[name] = value
		}
	}
	if id == localasr.PluginID && validateLocalVisionConfiguration(localVisionConfigurationFrom(pluginhost.Configuration{Values: values})) != nil {
		return
	}
	_, _ = host.controller.ConfigureIdle(ctx, id, pluginhost.ConfigUpdate{Revision: current.Revision, Values: values, Secrets: map[string]string{key: private[key]}})
}
