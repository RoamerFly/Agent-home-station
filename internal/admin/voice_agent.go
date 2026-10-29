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
	"wechat-bot/internal/workspacefs"
)

type voiceFile struct {
	File     string `json:"file,omitempty"`
	Path     string `json:"path,omitempty"`
	Filename string `json:"filename"`
	Hash     string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	Kind     string `json:"kind,omitempty"`
	Epoch    int    `json:"epoch,omitempty"`
}
type voiceEntry struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Engine        string            `json:"engine"`
	Status        string            `json:"status"`
	Notes         string            `json:"notes"`
	Weights       []voiceFile       `json:"weights"`
	References    []voiceFile       `json:"references"`
	Previews      []voiceFile       `json:"previews"`
	Recommended   map[string]string `json:"recommended_weights,omitempty"`
	Instruction   string            `json:"synthesis_instruction,omitempty"`
	Configuration string            `json:"configuration,omitempty"`
	ProfileID     string            `json:"profile_id,omitempty"`
	Public        bool              `json:"-"`
}

func readVoiceEntries(dir string) ([]voiceEntry, error) {
	r, err := os.OpenRoot(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := workspacefs.OpenRegular(r, "catalog.json")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var catalog struct {
		Items []voiceEntry `json:"items"`
	}
	if json.NewDecoder(io.LimitReader(f, 8<<20)).Decode(&catalog) != nil {
		return nil, errors.New("voice_catalog_invalid")
	}
	return catalog.Items, nil
}

func (p *permittedTools) accessibleVoices() ([]voiceEntry, error) {
	public, err := readVoiceEntries(filepath.Join(p.executionContext().Public, "models", "voice-library"))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range public {
		public[i].Public = true
		seen[public[i].ID] = true
	}
	if p.privileged() {
		private, err := readVoiceEntries(filepath.Join(p.manager.dir, "voice-library"))
		if err != nil {
			return nil, err
		}
		for _, v := range private {
			if !seen[v.ID] {
				public = append(public, v)
			}
		}
	}
	return public, nil
}

func voiceSummary(v voiceEntry) map[string]any {
	return map[string]any{"id": v.ID, "name": v.Name, "engine": v.Engine, "status": v.Status, "public": v.Public, "weight_files": len(v.Weights), "reference_files": len(v.References), "preview_files": len(v.Previews)}
}
func (p *permittedTools) voiceTools() []mcptools.Tool {
	return []mcptools.Tool{
		agentTool("voice_library_list", "Discover registered character voices by query/engine. Ordinary members see public voices; trusted owner also sees this account's private GPT-SoVITS/Bailian/RVC archive. No disk scan, synthesis or default changes. Paginated, default limit20; follow next_offset. Input {query?,engine?,offset?,limit?}.", `{"type":"object","properties":{"query":{"type":"string"},"engine":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		agentTool("voice_library_read", "Read exact discovered voice_id: weight versions, reference names, status and usage. No credentials/other accounts. Archived GPT-SoVITS is not proof of an installed TTS runtime. For shared RVC profile use returned music_rvc_start arguments, not default character voice. Input {voice_id:string}.", `{"type":"object","properties":{"voice_id":{"type":"string"}},"required":["voice_id"],"additionalProperties":false}`),
		agentTool("voice_library_prepare", "Materialize selected voice files with SHA256 checks for actual file/Shell access. Defaults recommended/latest weight per kind plus references (or previews); file_ids chooses exact hash.ext files from voice_library_read, including older epochs. Personal voices owner only: copy into account global workspace, then server_* global=true. Public models into current conversation. Never mounts credentials or whole private library, never executes weights or sends. Input {voice_id:string,file_ids?:string[]}.", `{"type":"object","properties":{"voice_id":{"type":"string"},"file_ids":{"type":"array","items":{"type":"string"},"minItems":1}},"required":["voice_id"],"additionalProperties":false}`),
	}
}

func (p *permittedTools) callVoiceLibrary(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var in struct {
		ID            string `json:"voice_id"`
		Query, Engine string
		Offset, Limit int
		Files         []string `json:"file_ids"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Offset < 0 || in.Limit < 0 {
		return "", toolInputError("invalid_voice_query")
	}
	voices, err := p.accessibleVoices()
	if err != nil {
		return "", err
	}
	encode := func(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
	if name == "voice_library_list" {
		rows := []map[string]any{}
		for _, v := range voices {
			if (in.Engine == "" || in.Engine == v.Engine) && strings.Contains(strings.ToLower(v.Name+" "+v.Engine), strings.ToLower(in.Query)) {
				rows = append(rows, voiceSummary(v))
			}
		}
		if in.Offset > len(rows) {
			return "", toolInputError("voice_offset_out_of_range")
		}
		if in.Limit == 0 {
			in.Limit = 20
		}
		end := in.Offset + min(in.Limit, len(rows)-in.Offset)
		return encode(map[string]any{"items": rows[in.Offset:end], "total": len(rows), "next_offset": end, "has_more": end < len(rows)})
	}
	for _, v := range voices {
		if v.ID != in.ID {
			continue
		}
		if !validID(v.ID) {
			return "", toolInputError("invalid_registered_voice_id")
		}
		files := append(append(append([]voiceFile{}, v.Weights...), v.References...), v.Previews...)
		for i := range files {
			if files[i].File == "" {
				files[i].File = files[i].Hash + filepath.Ext(files[i].Filename)
			}
		}
		if name == "voice_library_read" {
			row := voiceSummary(v)
			row["notes"], row["files"], row["recommended_weights"], row["synthesis_instruction"] = v.Notes, files, v.Recommended, v.Instruction
			row["usage"] = "voice_library_prepare取得可读模型和参考，然后project_list/project_read定位相应引擎。GPT-SoVITS、RVC和百炼音色不互换；权重齐全不等于运行环境和听感已经验证。"
			if v.Public && v.ProfileID != "" {
				row["music_rvc_start"] = map[string]string{"voice_profile_id": v.ProfileID}
			}
			return encode(row)
		}
		if !v.Public && (!p.privileged() || !p.manager.agent.config().CrossConversation) {
			return "", toolInputError("private_voice_prepare_requires_owner_account_global_access")
		}
		base := p.executionContext().Conversation
		if !v.Public {
			base = filepath.Join(p.executionContext().Private, "global")
		}
		dest := filepath.Join(base, "voice-library", v.ID)
		root, e := workspacefs.EnsurePath(dest)
		if e != nil {
			return "", e
		}
		defer root.Close()
		wanted := map[string]bool{}
		for _, id := range in.Files {
			wanted[id] = true
		}
		if len(in.Files) == 0 {
			for _, id := range v.Recommended {
				wanted[id] = true
			}
			if len(v.Recommended) == 0 {
				for _, f := range v.Weights {
					id := f.File
					if id == "" {
						id = f.Hash + filepath.Ext(f.Filename)
					}
					wanted[id] = true
				}
			}
			refs := v.References
			if len(refs) == 0 {
				refs = v.Previews
			}
			for _, f := range refs {
				id := f.File
				if id == "" {
					id = f.Hash + filepath.Ext(f.Filename)
				}
				wanted[id] = true
			}
		}
		selected := []voiceFile{}
		for _, f := range files {
			id := f.File
			if id == "" {
				id = f.Hash + filepath.Ext(f.Filename)
			}
			if !wanted[id] {
				continue
			}
			delete(wanted, id)
			f.File = id
			selected = append(selected, f)
		}
		if len(wanted) > 0 {
			return "", toolInputError("voice_file_not_registered")
		}
		prepared := []map[string]any{}
		for _, f := range selected {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if !voiceObjectName.MatchString(f.File) || len(f.Hash) != 64 || !strings.HasPrefix(f.File, f.Hash+".") || f.Bytes <= 0 {
				return "", toolInputError("invalid_voice_file_metadata")
			}
			sourceDir := filepath.Join(p.manager.dir, "voice-library", "files")
			relative := f.File
			if v.Public {
				sourceDir = filepath.Join(p.executionContext().Public, "models", "voice-library")
				relative, e = filepath.Rel(sourceDir, f.Path)
				if e != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
					return "", toolInputError("public_voice_path_outside_models")
				}
			}
			srcRoot, e := os.OpenRoot(sourceDir)
			if e != nil {
				return "", e
			}
			src, e := workspacefs.OpenRegular(srcRoot, relative)
			if e == nil {
				e = publishVerifiedStream(root, f.File, &uploadContextReader{ctx: ctx, reader: src}, f.Bytes, f.Hash)
				src.Close()
			}
			srcRoot.Close()
			if e != nil {
				return "", e
			}
			prepared = append(prepared, map[string]any{"file_id": f.File, "filename": f.Filename, "path": filepath.Join(dest, f.File), "sha256": f.Hash, "bytes": f.Bytes, "kind": f.Kind})
		}
		return encode(map[string]any{"voice_id": v.ID, "files": prepared, "global": !v.Public, "directory": dest, "engine": v.Engine, "status": v.Status, "synthesized": false, "wechat_sends": 0, "instruction": "可用server_read/list及隔离Shell读取本次准备的真实文件；私有条目用global=true。只准备文件，未运行模型，不修改默认音色。"})
	}
	return "", toolInputError("voice_not_found_or_not_authorized")
}
