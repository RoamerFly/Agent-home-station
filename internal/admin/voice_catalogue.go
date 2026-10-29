package admin

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"wechat-bot/internal/workspacefs"
)

func mergeVoiceCatalogue(private map[string]any, public string) (map[string]any, error) {
	shared := map[string]any{}
	if _, err := os.Lstat(public); err == nil {
		root, err := workspacefs.EnsurePath(public)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		f, err := workspacefs.OpenRegular(root, "catalog.json")
		if err == nil {
			defer f.Close()
			if err = json.NewDecoder(io.LimitReader(f, 8<<20)).Decode(&shared); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	items := []any{}
	seen, roles := map[string]bool{}, map[string]bool{}
	for _, source := range []struct {
		value map[string]any
		scope string
	}{{shared, "public"}, {private, "private"}} {
		rows, _ := source.value["items"].([]any)
		for _, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := row["id"].(string)
			if id != "" && seen[id] {
				continue
			}
			seen[id] = true
			row["library_scope"] = source.scope
			role, _ := row["role"].(string)
			if role == "" {
				role, _ = row["name"].(string)
			}
			row["role"] = role
			roles[role] = true
			for _, key := range []string{"weights", "references", "previews"} {
				files, _ := row[key].([]any)
				for _, rawFile := range files {
					file, ok := rawFile.(map[string]any)
					if !ok {
						continue
					}
					if file["file"] == nil || file["file"] == "" {
						hash, _ := file["sha256"].(string)
						filename, _ := file["filename"].(string)
						name := hash + filepath.Ext(filename)
						if voiceObjectName.MatchString(name) {
							file["file"] = name
						}
					}
				}
			}
			items = append(items, row)
		}
	}
	return map[string]any{"version": 1, "items": items, "role_count": len(roles)}, nil
}

func (m *Manager) voiceLibraryObject(name string) (string, string, error) {
	private := m.privateVoiceLibrary()
	public := filepath.Join(m.managedRoot(), "public_workspace", "models", "voice-library")
	for _, source := range []struct {
		directory string
		shared    bool
	}{{public, true}, {private, false}} {
		entries, err := readVoiceEntries(source.directory)
		if err != nil {
			return "", "", err
		}
		for _, item := range entries {
			for _, file := range append(append(append([]voiceFile{}, item.Weights...), item.References...), item.Previews...) {
				id := file.File
				if id == "" {
					id = file.Hash + filepath.Ext(file.Filename)
				}
				if id != name {
					continue
				}
				if !source.shared {
					relative, err := privateVoiceRelative(file)
					return source.directory, relative, err
				}
				relative, err := filepath.Rel(source.directory, file.Path)
				if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return "", "", os.ErrPermission
				}
				return source.directory, relative, nil
			}
		}
	}
	return private, filepath.Join("files", name), nil
}
