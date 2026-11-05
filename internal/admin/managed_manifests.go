package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
	"wechat-bot/internal/workspacefs"
)

// This is the registration metadata format, not a project/source read ceiling.
// Source files and large documentation remain available via paged server_read.
const projectManifestBytes = 65536

type projectManifestIssue struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func readProjectManifest(root *os.Root, name string) (json.RawMessage, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil, errors.New("project_not_registered")
	}
	if err != nil {
		return nil, errors.New("project_manifest_unavailable")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("project_manifest_not_regular")
	}
	f, err := workspacefs.OpenRegular(root, name)
	if err != nil {
		return nil, errors.New("project_manifest_unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, projectManifestBytes+1))
	if err != nil {
		return nil, errors.New("project_manifest_unavailable")
	}
	if len(data) > projectManifestBytes {
		return nil, errors.New("project_manifest_metadata_too_large")
	}
	var object map[string]json.RawMessage
	if !utf8.Valid(data) || json.Unmarshal(data, &object) != nil || object == nil {
		return nil, errors.New("project_manifest_invalid")
	}
	return json.RawMessage(data), nil
}

func readProjectManifests(ctx context.Context, public, id string, selected bool) ([]json.RawMessage, []projectManifestIssue, error) {
	if selected && (id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, "/\\:\x00")) {
		return nil, nil, toolInputError("project id must be one registered name")
	}
	// Pin the public root first. Resolving manifests through that handle cannot
	// escape to a control directory if the path is replaced during the call.
	publicRoot, err := os.OpenRoot(public)
	if err != nil {
		return nil, nil, err
	}
	defer publicRoot.Close()
	root, err := workspacefs.EnsureDirectory(publicRoot, "manifests")
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	items := []json.RawMessage{}
	issues := []projectManifestIssue{}
	if selected {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		data, err := readProjectManifest(root, id+".json")
		if err != nil {
			return nil, nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		return append(items, data), issues, nil
	}
	dir, err := workspacefs.OpenDirectory(root, ".")
	if err != nil {
		return nil, nil, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := readProjectManifest(root, entry.Name())
		if err != nil {
			issues = append(issues, projectManifestIssue{Name: entry.Name(), Reason: err.Error()})
			continue
		}
		items = append(items, data)
	}
	return items, issues, ctx.Err()
}
