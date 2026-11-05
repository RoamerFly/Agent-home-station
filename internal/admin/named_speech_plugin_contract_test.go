package admin

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/pluginhost"
)

// namedSpeechContractFixture deliberately uses the official, hash-verified
// speech package.  Its run hook replaces inference only: the policy still
// creates a durable job, verifies each registered input and parses a real WAV.
func namedSpeechContractFixture(t *testing.T) (*webFixture, *permittedTools, *namedSpeechPluginPolicy, pluginhost.Job, pluginhost.Capability) {
	t.Helper()
	f, access := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.Host.Mode = "full"
	// Private voice material retains the existing global owner gate.  The
	// named-speech adapter must not make this fixture pass by bypassing it.
	cfg.CrossConversation = true
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.Companion.Permissions.SendVoice = true

	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), cleanSpeechPackageFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveConfiguration(t.Context(), "speech", pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{
		"local_profile_id": json.RawMessage(`"gpt-sovits"`),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(t.Context(), "speech", installed.Revision, true); err != nil {
		t.Fatal(err)
	}

	x := access.executionContext()
	project := filepath.Join(x.Public, "projects", "synthetic-gpt-sovits")
	models := filepath.Join(x.Public, "models", "synthetic-gpt-sovits")
	if err = ensureManaged(project, models, filepath.Join(x.Public, "manifests")); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(project, "python")
	if err = writePrivate(python, []byte("synthetic-managed-interpreter")); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]string{
		"id": "gpt-sovits", "root": project, "python": python, "base_models": models,
	})
	if err = writePrivate(filepath.Join(x.Public, "manifests", "gpt-sovits.json"), manifest); err != nil {
		t.Fatal(err)
	}

	root := f.manager.privateVoiceLibrary()
	if err = ensureManaged(filepath.Join(root, "files")); err != nil {
		t.Fatal(err)
	}
	add := func(name, kind string, body []byte) voiceFile {
		t.Helper()
		sum := sha256.Sum256(body)
		id := hex.EncodeToString(sum[:]) + filepath.Ext(name)
		if err := writePrivate(filepath.Join(root, "files", id), body); err != nil {
			t.Fatal(err)
		}
		return voiceFile{File: id, Filename: name, Kind: kind, Hash: hex.EncodeToString(sum[:]), Bytes: int64(len(body))}
	}
	gpt := add("voice.ckpt", "gpt", []byte("synthetic gpt weights"))
	sovits := add("voice.pth", "sovits", []byte("synthetic sovits weights"))
	reference := add("reference.wav", "reference", syntheticNamedSpeechWAV())
	prompt := add("reference.txt", "reference", []byte("\ufeff 合成参考文本 "))
	voice := voiceEntry{
		ID: "synthetic-named-voice", Name: "Synthetic named voice", Engine: "GPT-SoVITS", Status: "ready",
		Weights: []voiceFile{gpt, sovits}, References: []voiceFile{reference, prompt},
		Recommended: map[string]string{"gpt": voiceFileID(gpt), "sovits": voiceFileID(sovits)},
	}
	catalog, _ := json.Marshal(map[string]any{"items": []voiceEntry{voice}})
	if err = writePrivate(filepath.Join(root, "catalog.json"), catalog); err != nil {
		t.Fatal(err)
	}

	raw := json.RawMessage(`{"voice_id":"synthetic-named-voice","text":"离线命名声线契约","device":"cpu","version":"v2","parameters":{"sample_steps":7},"timeout_seconds":31}`)
	policy, err := access.newNamedSpeechPluginPolicy(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	job, created, err := store.CreateJob(t.Context(), pluginhost.Job{
		Plugin: "speech", Tool: "speech.synthesize_named", Version: policy.packageVersion,
		PackageDigest: policy.packageDigest, ConfigRevision: "1", Context: policy.invocation,
		Arguments: append(json.RawMessage(nil), policy.arguments...),
	})
	if err != nil || !created {
		t.Fatal("failed to create named-speech durable job", err, created)
	}
	cap := pluginhost.Capability{ID: "speech.synthesize_named", Permissions: []string{"artifact.read", "model.invoke"}}
	return f, access, policy, job, cap
}

// Package verification intentionally rejects undeclared files.  Python may
// leave a local __pycache__ behind when a developer runs a plugin script by
// hand, so fixtures copy only declared-source candidates instead of turning
// that transient cache into a package integrity exception unrelated to the
// contract being tested.  Production execution sets PYTHONDONTWRITEBYTECODE
// and still verifies the installed package itself.
func cleanSpeechPackageFixture(t *testing.T) string {
	t.Helper()
	from := "../../plugins/speech"
	to := t.TempDir()
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(to, rel), 0700)
		}
		if !d.Type().IsRegular() || filepath.Ext(d.Name()) == ".pyc" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return to
}

func syntheticNamedSpeechWAV() []byte {
	// PCM mono, 8 kHz, two samples.  It is deliberately a real, minimal WAV so
	// the host's independent header/duration verification is exercised.
	const samples = 2
	b := make([]byte, 44+samples*2)
	copy(b[0:4], "RIFF")
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	binary.LittleEndian.PutUint32(b[16:20], 16)
	binary.LittleEndian.PutUint16(b[20:22], 1)
	binary.LittleEndian.PutUint16(b[22:24], 1)
	binary.LittleEndian.PutUint32(b[24:28], 8000)
	binary.LittleEndian.PutUint32(b[28:32], 16000)
	binary.LittleEndian.PutUint16(b[32:34], 2)
	binary.LittleEndian.PutUint16(b[34:36], 16)
	copy(b[36:40], "data")
	binary.LittleEndian.PutUint32(b[40:44], samples*2)
	binary.LittleEndian.PutUint16(b[44:46], 1200)
	binary.LittleEndian.PutUint16(b[46:48], 0)
	return b
}

func namedSpeechArg(argv []string, key string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == key {
			return argv[i+1]
		}
	}
	return ""
}

func namedSpeechSyntheticRunner(t *testing.T, p *namedSpeechPluginPolicy, calls *int) func(context.Context, agenthost.Config, string, []string) ([]byte, []byte, error) {
	t.Helper()
	return func(_ context.Context, cfg agenthost.Config, cwd string, argv []string) ([]byte, []byte, error) {
		*calls++
		if cwd == "" || len(cfg.Roots) == 0 || namedSpeechArg(argv, "--output") == "" || namedSpeechArg(argv, "--gpt") == "" || namedSpeechArg(argv, "--sovits") == "" || namedSpeechArg(argv, "--reference") == "" || namedSpeechArg(argv, "--prompt-text-file") == "" {
			t.Fatal("named speech runner lost host-bound arguments", argv, cfg)
		}
		output := namedSpeechArg(argv, "--output")
		wav := syntheticNamedSpeechWAV()
		if p.targetID != "" {
			wav = syntheticReferenceWAV(t, 2)
		}
		if err := os.WriteFile(output, wav, 0600); err != nil {
			t.Fatal(err)
		}
		report := namedSpeechRunnerResult{
			Output: output, SampleRate: 8000, Seconds: float64(len(wav)-44) / 16000, SHA256: namedSpeechSHA(wav),
			Device: p.request.Device, Version: p.request.Version, GPT: namedSpeechArg(argv, "--gpt"), SoVITS: namedSpeechArg(argv, "--sovits"),
			GPTSHA256: p.resource.GPT.Hash, SoVITSSHA256: p.resource.SoVITS.Hash, ReferenceSHA256: p.resource.Reference.Hash,
			PromptSHA256: namedSpeechSHA([]byte("合成参考文本")), TextSHA256: namedSpeechSHA([]byte(p.request.Text)), WeChatSends: 0,
		}
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		return raw, nil, nil
	}
}

func TestNamedSpeechPolicyBindsRegisteredVoiceAndSingleSyntheticInference(t *testing.T) {
	_, _, p, job, cap := namedSpeechContractFixture(t)
	calls := 0
	p.run = namedSpeechSyntheticRunner(t, p, &calls)
	result, err := p.Service(t.Context(), job, cap, "wechat/local_speech", p.arguments)
	if err != nil || calls != 1 {
		t.Fatal("named speech broker did not complete exactly once", err, calls)
	}
	if _, err = p.Service(t.Context(), job, cap, "wechat/local_speech", p.arguments); err == nil || calls != 1 {
		t.Fatal("named speech broker replayed an accepted inference", err, calls)
	}
	verified, err := p.Validate(t.Context(), job, cap, result)
	if err != nil || !strings.Contains(string(verified.Result), `"status":"synthesized"`) || len(verified.Private) == 0 {
		t.Fatal("named speech result did not retain the verified private checkpoint", err, string(verified.Result), string(verified.Private))
	}
	data, err := p.readCheckpoint(t.Context(), p.checkpoint)
	if err != nil || string(data) != string(syntheticNamedSpeechWAV()) {
		t.Fatal("named speech checkpoint did not retain actual WAV", err)
	}
}
