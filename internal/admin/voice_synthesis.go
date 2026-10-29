package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/workspacefs"
)

// Library synthesis uses the same registered source/CLI available to the
// Agent. It executes with argv, in the OS sandbox, and only stages media.
// A named voice never falls through to the account's default provider voice.
func (p *permittedTools) synthesizeLibraryVoice(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		VoiceID               string `json:"voice_id"`
		Text, Device, Version string
		Files                 []string       `json:"file_ids"`
		Parameters            map[string]any `json:"parameters"`
		Timeout               float64        `json:"timeout_seconds"`
	}
	if json.Unmarshal(raw, &in) != nil || in.VoiceID == "" || strings.TrimSpace(in.Text) == "" || in.Timeout < 0 || math.IsNaN(in.Timeout) || math.IsInf(in.Timeout, 0) || in.Timeout > float64((1<<63-1)/int64(time.Second)) {
		return "", toolInputError("invalid_library_speech_parameters")
	}
	if !p.workspaceExecutionAllowed() || p.manager.agent.config().Host.Mode != "full" || !p.manager.Config().Companion.Permissions.SendVoice {
		return "", toolInputError("library_voice_requires_authorized_workspace_execution_and_voice_permission")
	}
	voices, err := p.accessibleVoices()
	if err != nil {
		return "", err
	}
	var selected *voiceEntry
	for i := range voices {
		if voices[i].ID == in.VoiceID {
			selected = &voices[i]
			break
		}
	}
	if selected == nil {
		return "", toolInputError("voice_not_found_or_not_authorized")
	}
	if !strings.EqualFold(selected.Engine, "GPT-SoVITS") {
		return "", toolInputError("named_voice_engine_has_no_registered_text_synthesis_route; inspect_voice_library_read")
	}
	x := p.executionContext()
	manifests, _, err := readProjectManifests(ctx, x.Public, "gpt-sovits", true)
	if err != nil {
		return "", fmt.Errorf("gpt_sovits_registration: %w", err)
	}
	var project struct {
		Root, Python, Entry string
		Models              string `json:"base_models"`
	}
	if len(manifests) != 1 || json.Unmarshal(manifests[0], &project) != nil {
		return "", toolInputError("gpt_sovits_manifest_invalid")
	}
	for _, path := range []string{project.Root, project.Python, project.Entry, project.Models} {
		rel, e := filepath.Rel(x.Public, path)
		if e != nil || !filepath.IsAbs(path) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", toolInputError("gpt_sovits_resource_outside_public_workspace")
		}
	}
	args, _ := json.Marshal(map[string]any{"voice_id": in.VoiceID, "file_ids": in.Files})
	prepared, err := p.callVoiceLibrary(ctx, "voice_library_prepare", args)
	if err != nil {
		return "", err
	}
	var library struct {
		Directory string
		Files     []struct{ Path, Kind, Filename string }
	}
	if json.Unmarshal([]byte(prepared), &library) != nil {
		return "", errors.New("prepared_voice_response_invalid")
	}
	gpt, sovits, reference, prompt := "", "", "", ""
	for _, f := range library.Files {
		switch f.Kind {
		case "gpt":
			if gpt != "" {
				return "", toolInputError("select_one_gpt_weight_with_file_ids")
			}
			gpt = f.Path
		case "sovits":
			if sovits != "" {
				return "", toolInputError("select_one_sovits_weight_with_file_ids")
			}
			sovits = f.Path
		}
		switch strings.ToLower(filepath.Ext(f.Filename)) {
		case ".wav", ".mp3", ".flac", ".m4a":
			if reference == "" {
				reference = f.Path
			}
		case ".txt":
			if prompt == "" {
				prompt = f.Path
			}
		}
	}
	if gpt == "" || sovits == "" || reference == "" || prompt == "" {
		return "", toolInputError("gpt_sovits_requires_paired_weights_reference_audio_and_reference_transcript; select_file_ids_or_read_project")
	}
	output := filepath.Join(x.Conversation, "output")
	if err = ensureManaged(output); err != nil {
		return "", err
	}
	folder, err := os.MkdirTemp(output, "speech-")
	if err != nil {
		return "", err
	}
	// Preserve the result and execution log for diagnosis/reuse, even on error.
	dest := filepath.Join(folder, "speech.wav")
	if in.Device == "" {
		in.Device = "cuda"
	}
	if in.Version == "" {
		in.Version = "v2"
	}
	if in.Timeout == 0 {
		in.Timeout = 600
	}
	cfg, err := p.hostConfig()
	if err != nil {
		return "", err
	}
	cfg.Roots = []string{folder}
	cfg.Isolation.GPU = strings.HasPrefix(in.Device, "cuda")
	cfg.Isolation.ReadOnly = []string{x.Public, library.Directory}
	argv := []string{project.Python, project.Entry, "--project", project.Root, "--models", project.Models, "--gpt", gpt, "--sovits", sovits, "--reference", reference, "--prompt-text-file", prompt, "--text", in.Text, "--device", in.Device, "--version", in.Version, "--output", dest}
	if in.Device == "cpu" {
		argv = append(argv, "--no-half")
	}
	if len(in.Parameters) > 0 {
		data, e := json.Marshal(in.Parameters)
		if e != nil {
			return "", e
		}
		path := filepath.Join(folder, "parameters.json")
		if e = os.WriteFile(path, data, 0600); e != nil {
			return "", e
		}
		argv = append(argv, "--parameters", path)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(in.Timeout*float64(time.Second)))
	defer cancel()
	// Keep upstream diagnostics privately; the generic sandbox omits stderr.
	wrapper := "import contextlib,runpy,sys\nlog=sys.argv.pop(1)\nsys.argv=sys.argv[1:]\nwith open(log,'a',encoding='utf-8') as f, contextlib.redirect_stderr(f):\n runpy.run_path(sys.argv[0],run_name='__main__')\n"
	argv = append([]string{project.Python, "-u", "-c", wrapper, filepath.Join(folder, "stderr.log")}, argv[1:]...)
	log, runErr := agenthost.RunIsolated(bounded, cfg, folder, argv)
	if err = os.WriteFile(filepath.Join(folder, "execution.log"), log, 0600); err != nil {
		return "", err
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("gpt_sovits_inference_failed: %w; execution log: %s", runErr, filepath.Join(folder, "execution.log"))
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, "speech.wav")
	if err != nil {
		return "", errors.New("gpt_sovits_output_missing")
	}
	defer file.Close()
	limit := toolsLimit("file")
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if len(data) < 44 || int64(len(data)) > limit || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return "", errors.New("gpt_sovits_output_not_valid_wav_or_exceeds_media_capacity")
	}
	out, err := preparePublicAudioPreview(ctx, p.manager.dir, data, 0)
	if err != nil {
		return "", err
	}
	result, err := p.stageOutputWithAsset(ctx, out, &assetOriginal{VoiceComplete: true, Data: data, MIME: "audio/wav", Filename: selected.Name + "-语音.wav"})
	if err != nil {
		return "", err
	}
	var value map[string]any
	if json.Unmarshal([]byte(result), &value) != nil {
		return "", errors.New("staged_voice_response_invalid")
	}
	value["voice_id"], value["voice_name"], value["engine"], value["output_path"] = selected.ID, selected.Name, selected.Engine, dest
	value["synthesized"], value["wechat_sends"] = true, 0
	encoded, err := json.Marshal(value)
	return string(encoded), err
}
