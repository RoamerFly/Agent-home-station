package admin

// Trusted resource selection for a named local speech request.  It resolves
// catalogue entries in the host only; none of these paths are plugin arguments.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"wechat-bot/internal/workspacefs"
)

// namedSpeechManifestResourceProof is read-only migration evidence. It does
// not inspect an account voice catalogue or expose any filesystem location.
func namedSpeechManifestResourceProof(ctx context.Context, public string) (string, bool, error) {
	items, _, err := readProjectManifests(ctx, public, "gpt-sovits", true)
	if err != nil || len(items) != 1 {
		return "", false, nil
	}
	var manifest struct {
		Root     string `json:"root"`
		Python   string `json:"python"`
		Models   string `json:"base_models"`
		Packages string `json:"packages"`
	}
	if json.Unmarshal(items[0], &manifest) != nil || manifest.Root == "" || manifest.Python == "" || manifest.Models == "" {
		return "", false, nil
	}
	for _, path := range []string{manifest.Root, manifest.Python, manifest.Models} {
		if !filepath.IsAbs(path) || !pathWithin(public, path) {
			return "", false, nil
		}
		if _, err := os.Stat(path); err != nil {
			return "", false, nil
		}
	}
	if manifest.Packages != "" && (!pathWithin(public, manifest.Packages) || regularMusicPath(manifest.Packages, true) != nil) {
		return "", false, nil
	}
	if regularMusicPath(manifest.Root, true) != nil || regularMusicPath(manifest.Models, true) != nil || regularMusicPath(manifest.Python, false) != nil {
		return "", false, nil
	}
	python, err := os.ReadFile(manifest.Python)
	if err != nil || len(python) == 0 {
		return "", false, nil
	}
	b, _ := json.Marshal([]any{json.RawMessage(items[0]), sha256.Sum256(python)})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true, nil
}

var namedSpeechResourceValuePattern = regexp.MustCompile(`(?i)(^[a-z]:|://|\.(ckpt|pth|pt|safetensors|wav|mp3|flac|json|yaml|yml)$)`)

func validateNamedSpeechParameters(value map[string]any) error {
	protected := map[string]bool{"text": true, "prompt_text": true, "device": true, "version": true, "is_half": true, "half": true, "streaming_mode": true, "return_fragment": true, "gpt_streaming": true, "stream": true, "streaming": true, "aux_ref_audio_paths": true, "reference": true, "references": true, "ref_audio": true, "audio": true, "gpt": true, "sovits": true, "project": true, "models": true, "packages": true, "output": true, "overwrite": true}
	resourceToken := map[string]bool{"path": true, "paths": true, "file": true, "files": true, "dir": true, "directory": true, "url": true, "uri": true, "weight": true, "weights": true, "checkpoint": true, "ckpt": true, "model": true, "models": true, "reference": true, "credentials": true, "secret": true}
	var inspect func(any) error
	normalize := func(key string) string {
		var out []rune
		for i, r := range key {
			if i > 0 && unicode.IsUpper(r) {
				out = append(out, '_')
			}
			if r == '-' {
				r = '_'
			}
			out = append(out, unicode.ToLower(r))
		}
		return string(out)
	}
	inspect = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				n := normalize(key)
				tokens := strings.Split(n, "_")
				blocked := protected[n]
				for _, token := range tokens {
					blocked = blocked || resourceToken[token]
				}
				if blocked {
					return errors.New("protected_synthesis_parameter")
				}
				if err := inspect(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range x {
				if err := inspect(child); err != nil {
					return err
				}
			}
		case string:
			if namedSpeechResourceValuePattern.MatchString(x) {
				return errors.New("resource_parameter_not_allowed")
			}
			if strings.ContainsRune(x, 0) || strings.ContainsAny(x, `/\\`) || strings.Contains(x, "://") {
				return errors.New("resource_parameter_not_allowed")
			}
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return errors.New("invalid_parameter_value")
			}
		}
		return nil
	}
	return inspect(value)
}

type namedSpeechResource struct {
	Voice             voiceEntry
	GPT, SoVITS       voiceFile
	Reference, Prompt voiceFile
	Version           string
}

func voiceFileID(f voiceFile) string {
	if f.File != "" {
		return f.File
	}
	return f.Hash + filepath.Ext(f.Filename)
}

func (p *permittedTools) namedSpeechResource(ctx context.Context, id string, files []string) (namedSpeechResource, error) {
	voices, err := p.accessibleVoices()
	if err != nil {
		return namedSpeechResource{}, err
	}
	var voice *voiceEntry
	for i := range voices {
		if voices[i].ID == id {
			voice = &voices[i]
			break
		}
	}
	if voice == nil || !strings.EqualFold(voice.Engine, "GPT-SoVITS") {
		return namedSpeechResource{}, toolInputError("voice_not_found_or_not_authorized")
	}
	if !voice.Public && (!p.privileged() || !p.manager.agent.config().CrossConversation) {
		return namedSpeechResource{}, toolInputError("private_voice_prepare_requires_owner_account_global_access")
	}
	wanted := map[string]bool{}
	for _, file := range files {
		if file == "" || wanted[file] {
			return namedSpeechResource{}, toolInputError("invalid_voice_file_selection")
		}
		wanted[file] = true
	}
	if len(wanted) > 0 {
		known := map[string]bool{}
		counts := map[string]int{}
		for _, f := range append(append([]voiceFile{}, voice.Weights...), voice.References...) {
			if wanted[voiceFileID(f)] {
				known[voiceFileID(f)] = true
				if f.Kind == "gpt" || f.Kind == "sovits" {
					counts[f.Kind]++
				}
			}
		}
		for id := range wanted {
			if !known[id] {
				return namedSpeechResource{}, toolInputError("unregistered_voice_file_selection")
			}
		}
		if counts["gpt"] > 1 || counts["sovits"] > 1 {
			return namedSpeechResource{}, toolInputError("select_one_gpt_and_sovits_weight_with_file_ids")
		}
	}
	selectFile := func(kind string, all []voiceFile) (voiceFile, bool) {
		if len(wanted) == 0 && voice.Recommended != nil && voice.Recommended[kind] != "" {
			for _, f := range all {
				if f.Kind == kind && voiceFileID(f) == voice.Recommended[kind] {
					return f, true
				}
			}
		}
		for _, f := range all {
			if f.Kind == kind && (len(wanted) == 0 || wanted[voiceFileID(f)]) {
				return f, true
			}
		}
		return voiceFile{}, false
	}
	gpt, gok := selectFile("gpt", voice.Weights)
	sovits, sok := selectFile("sovits", voice.Weights)
	var reference, prompt voiceFile
	for _, f := range voice.References {
		ext := strings.ToLower(filepath.Ext(f.Filename))
		if (ext == ".wav" || ext == ".mp3" || ext == ".flac" || ext == ".m4a") && reference.Hash == "" && (len(wanted) == 0 || wanted[voiceFileID(f)]) {
			reference = f
		}
		if ext == ".txt" && prompt.Hash == "" && (len(wanted) == 0 || wanted[voiceFileID(f)]) {
			prompt = f
		}
	}
	if !gok || !sok || reference.Hash == "" || prompt.Hash == "" {
		return namedSpeechResource{}, toolInputError("gpt_sovits_requires_paired_weights_reference_audio_and_reference_transcript")
	}
	if err := p.verifyNamedSpeechFiles(ctx, *voice, []voiceFile{gpt, sovits, reference, prompt}); err != nil {
		return namedSpeechResource{}, err
	}
	b, _ := json.Marshal([]any{voice.ID, voice.Engine, voice.Public, voice.Status, voice.Configuration, voice.Recommended, gpt, sovits, reference, prompt})
	sum := sha256.Sum256(b)
	return namedSpeechResource{Voice: *voice, GPT: gpt, SoVITS: sovits, Reference: reference, Prompt: prompt, Version: hex.EncodeToString(sum[:])}, nil
}

func (p *permittedTools) verifyNamedSpeechFiles(ctx context.Context, voice voiceEntry, files []voiceFile) error {
	base := p.manager.privateVoiceLibrary()
	if voice.Public {
		base = filepath.Join(p.executionContext().Public, "models", "voice-library")
	}
	root, err := workspacefs.EnsurePath(base)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, item := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(item.Hash) != 64 || item.Bytes < 1 {
			return errors.New("named_speech_resource_metadata_invalid")
		}
		rel, err := privateVoiceRelative(item)
		if voice.Public {
			rel, err = publicVoiceRelative(base, item)
			if err != nil {
				return errors.New("named_speech_resource_path_invalid")
			}
		}
		if err != nil {
			return err
		}
		f, err := workspacefs.OpenRegular(root, rel)
		if err != nil {
			return errors.New("named_speech_resource_unavailable")
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, io.LimitReader(f, item.Bytes+1))
		f.Close()
		if copyErr != nil || n != item.Bytes || hex.EncodeToString(h.Sum(nil)) != item.Hash {
			return errors.New("named_speech_resource_changed")
		}
	}
	return nil
}

// namedSpeechFilePath is deliberately private and used only by the host broker
// after the same hash checks. It is never serialized into plugin calls/results.
func (p *permittedTools) namedSpeechFilePath(voice voiceEntry, f voiceFile) (string, error) {
	base := p.manager.privateVoiceLibrary()
	if voice.Public {
		base = filepath.Join(p.executionContext().Public, "models", "voice-library")
	}
	rel, err := privateVoiceRelative(f)
	if voice.Public {
		rel, err = publicVoiceRelative(base, f)
	}
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("named_speech_resource_path_invalid")
	}
	path := filepath.Join(base, rel)
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("named_speech_resource_unavailable")
	}
	return path, nil
}
