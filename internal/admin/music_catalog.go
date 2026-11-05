package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/workspacefs"
)

const musicToolCatalogFile = "runtime/tool_catalog.json"
const musicNativeSamplingFile = "runtime/native_sampling.json"

// Missing, disabled or modified installations never fall back to built-in text.
func (m *Manager) musicCatalogResources(ctx context.Context, names ...string) (map[string]json.RawMessage, error) {
	s, err := m.packageStore()
	if err != nil {
		return nil, err
	}
	i, err := s.Installed(ctx, "music")
	if err != nil || !i.Enabled || i.Removed {
		return nil, errors.New("music_plugin_catalog_unavailable")
	}
	dir, err := s.PackageDirectory(i.Digest)
	if err != nil {
		return nil, err
	}
	if _, err = pluginhost.Verify(dir); err != nil {
		return nil, errors.New("music_plugin_catalog_integrity_changed")
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil || namedSpeechSHA(manifest) != i.Digest {
		return nil, errors.New("music_plugin_catalog_integrity_changed")
	}
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	out := map[string]json.RawMessage{}
	for _, name := range names {
		expected := i.Package.Files[name]
		if len(expected) != 64 {
			return nil, errors.New("music_plugin_catalog_unavailable")
		}
		f, err := workspacefs.OpenRegular(root, filepath.FromSlash(name))
		if err != nil {
			return nil, errors.New("music_plugin_catalog_unavailable")
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(raw) > 1<<20 || namedSpeechSHA(raw) != expected {
			return nil, errors.New("music_plugin_catalog_integrity_changed")
		}
		out[name] = raw
	}
	current, err := s.Installed(ctx, "music")
	if err != nil || !current.Enabled || current.Removed || current.Digest != i.Digest || current.Revision != i.Revision {
		return nil, errors.New("music_plugin_catalog_changed")
	}
	return out, nil
}

func musicNativeDefinitions(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var document struct {
		Version     int                        `json:"version"`
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	if musicStrictJSON(raw, &document) != nil || document.Version != 1 || len(document.Definitions) == 0 {
		return nil, errors.New("music_plugin_catalog_invalid")
	}
	return document.Definitions, nil
}

func resolveMusicCatalogSchema(value any, definitions map[string]json.RawMessage, depth int) (any, error) {
	if depth > 32 {
		return nil, errors.New("music_plugin_catalog_reference_limit")
	}
	switch v := value.(type) {
	case map[string]any:
		if ref, ok := v["$ref"]; ok {
			name, ok := ref.(string)
			const prefix = "native_sampling.json#/definitions/"
			if !ok || len(v) != 1 || !strings.HasPrefix(name, prefix) {
				return nil, errors.New("music_plugin_catalog_reference_invalid")
			}
			raw, ok := definitions[strings.TrimPrefix(name, prefix)]
			var resolved any
			if !ok || json.Unmarshal(raw, &resolved) != nil {
				return nil, errors.New("music_plugin_catalog_reference_invalid")
			}
			return resolveMusicCatalogSchema(resolved, definitions, depth+1)
		}
		for key, item := range v {
			resolved, err := resolveMusicCatalogSchema(item, definitions, depth+1)
			if err != nil {
				return nil, err
			}
			v[key] = resolved
		}
	case []any:
		for index, item := range v {
			resolved, err := resolveMusicCatalogSchema(item, definitions, depth+1)
			if err != nil {
				return nil, err
			}
			v[index] = resolved
		}
	}
	return value, nil
}

func (m *Manager) musicToolCatalog(ctx context.Context) ([]mcptools.Tool, error) {
	resources, err := m.musicCatalogResources(ctx, musicToolCatalogFile, musicNativeSamplingFile)
	if err != nil {
		return nil, err
	}
	definitions, err := musicNativeDefinitions(resources[musicNativeSamplingFile])
	if err != nil {
		return nil, err
	}
	var document struct {
		Version int             `json:"version"`
		Tools   []mcptools.Tool `json:"tools"`
	}
	if musicStrictJSON(resources[musicToolCatalogFile], &document) != nil || document.Version != 1 || len(document.Tools) == 0 {
		return nil, errors.New("music_plugin_catalog_invalid")
	}
	seen := map[string]bool{}
	for index, tool := range document.Tools {
		var schema map[string]any
		if !musicIdentifier(tool.Name) || seen[tool.Name] || strings.TrimSpace(tool.Description) == "" || json.Unmarshal(tool.InputSchema, &schema) != nil || schema["type"] != "object" {
			return nil, errors.New("music_plugin_catalog_invalid")
		}
		seen[tool.Name] = true
		resolved, err := resolveMusicCatalogSchema(schema, definitions, 0)
		if err != nil {
			return nil, err
		}
		document.Tools[index].InputSchema, err = json.Marshal(resolved)
		if err != nil {
			return nil, err
		}
	}
	return document.Tools, nil
}
