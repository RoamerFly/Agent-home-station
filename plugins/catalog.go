// Package catalog embeds descriptive package contracts, not executable runtimes.
package catalog

import (
	"embed"
	"encoding/json"
	"io/fs"
	"sort"
	"wechat-bot/internal/pluginhost"
)

//go:embed */plugin.json
var manifests embed.FS

func List() ([]pluginhost.Package, error) {
	names, err := fs.Glob(manifests, "*/plugin.json")
	if err != nil {
		return nil, err
	}
	result := make([]pluginhost.Package, 0, len(names))
	for _, name := range names {
		b, err := manifests.ReadFile(name)
		if err != nil {
			return nil, err
		}
		var p pluginhost.Package
		if err = json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
