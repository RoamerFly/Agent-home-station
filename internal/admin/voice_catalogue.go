package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	directory, relative, _, err := m.voiceLibraryObjectMetadata(name)
	return directory, relative, err
}

func (m *Manager) voiceLibraryObjectMetadata(name string) (string, string, *voiceFile, error) {
	private := m.privateVoiceLibrary()
	public := filepath.Join(m.managedRoot(), "public_workspace", "models", "voice-library")
	for _, source := range []struct {
		directory string
		shared    bool
	}{{public, true}, {private, false}} {
		entries, err := readVoiceEntries(source.directory)
		if err != nil {
			return "", "", nil, err
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
					return source.directory, relative, nil, err
				}
				relative, err := publicVoiceRelative(source.directory, file)
				if err != nil {
					return "", "", nil, err
				}
				return source.directory, relative, &file, nil
			}
		}
	}
	return private, filepath.Join("files", name), nil, nil
}

// Relative registrations are portable across a Linux/Windows deployment. An
// obsolete absolute path is never used to widen the current public model root.
func publicVoiceRelative(public string, file voiceFile) (string, error) {
	if file.Relative != "" {
		normalized := strings.ReplaceAll(file.Relative, `\`, "/")
		if strings.HasPrefix(normalized, "/") || strings.ContainsAny(normalized, ":\x00") {
			return "", os.ErrPermission
		}
		for _, part := range strings.Split(normalized, "/") {
			if part == "" || part == "." || part == ".." {
				return "", os.ErrPermission
			}
		}
		return filepath.FromSlash(normalized), nil
	}
	if !filepath.IsAbs(file.Path) {
		return "", os.ErrPermission
	}
	relative, err := filepath.Rel(public, file.Path)
	if err != nil || filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return relative, nil
}

func (m *Manager) openVoiceLibraryObject(ctx context.Context, name string) (*os.File, int64, error) {
	directory, relative, registered, err := m.voiceLibraryObjectMetadata(name)
	if err != nil {
		return nil, 0, err
	}
	root, err := workspacefs.EnsurePath(directory)
	if err != nil {
		return nil, 0, err
	}
	defer root.Close()
	f, err := workspacefs.OpenRegular(root, relative)
	if err != nil {
		return nil, 0, err
	}
	if registered == nil { // Preserve the account-private catalogue/file scope.
		return f, -1, nil
	}
	fail := func() (*os.File, int64, error) { f.Close(); return nil, 0, errors.New("public_voice_file_changed") }
	info, err := f.Stat()
	if err != nil || registered.Bytes <= 0 || info.Size() != registered.Bytes || len(registered.Hash) != 64 || strings.Trim(registered.Hash, "0123456789abcdef") != "" || !strings.HasPrefix(name, registered.Hash+".") {
		return fail()
	}
	h := sha256.New()
	n, err := io.Copy(h, &uploadContextReader{ctx: ctx, reader: io.LimitReader(f, registered.Bytes)})
	if err != nil || n != registered.Bytes || hex.EncodeToString(h.Sum(nil)) != registered.Hash {
		return fail()
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return fail()
	}
	return f, registered.Bytes, nil // Serve this verified handle and byte range.
}
