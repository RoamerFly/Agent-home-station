package admin

// Host-side runner for the private GPT-SoVITS enrollment worker.  The Python
// package receives only a job-local manifest and read-only registered runtime
// resources.  It never receives a voice-library directory, WeChat credentials,
// or a way to deliver a result.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/workspacefs"
)

type enrollmentResult struct {
	Dir            string
	ReferenceText  string
	PreviewSHA     string
	ReferenceSHA   string
	GPTSHA         string
	SoVITSSHA      string
	Reason         string
	SampleSeconds  float64
	PreviewSeconds float64
}

type enrollmentModelLock struct {
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
}

type enrollmentModelLocks struct {
	SchemaVersion int                 `json:"schema_version"`
	Engine        string              `json:"engine"`
	EngineVersion string              `json:"engine_version"`
	GPT           enrollmentModelLock `json:"gpt"`
	SoVITS        enrollmentModelLock `json:"sovits"`
	Discriminator enrollmentModelLock `json:"discriminator"`
}

type enrollmentWorkerResult struct {
	State          string  `json:"state"`
	Reason         string  `json:"reason"`
	JobID          string  `json:"job_id"`
	ProfileID      string  `json:"profile_id"`
	Revision       int     `json:"revision"`
	WechatSends    int     `json:"wechat_sends"`
	PreviewSHA     string  `json:"preview_sha256"`
	ReferenceSHA   string  `json:"reference_sha256"`
	ReferenceText  string  `json:"reference_text"`
	GPTSHA         string  `json:"gpt_sha256"`
	SoVITSSHA      string  `json:"sovits_sha256"`
	SampleSeconds  float64 `json:"sample_seconds"`
	PreviewSeconds float64 `json:"seconds"`
	SpeakerCheck   string  `json:"speaker_check"`
}

func enrollmentSHAFile(path string, wantBytes int64) (string, error) {
	if err := regularMusicPath(path, false); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil || n < 1 || wantBytes > 0 && n != wantBytes {
		return "", errors.New("voice_enrollment_file_integrity_failed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func enrollmentLockedPath(base string, lock enrollmentModelLock) (string, error) {
	if lock.Bytes < 1 || len(lock.SHA256) != 64 || strings.Trim(lock.SHA256, "0123456789abcdef") != "" || lock.RelativePath == "" || filepath.IsAbs(lock.RelativePath) {
		return "", errors.New("voice_enrollment_base_model_lock_invalid")
	}
	path := filepath.Join(base, filepath.FromSlash(lock.RelativePath))
	if !pathWithin(base, path) {
		return "", errors.New("voice_enrollment_base_model_path_invalid")
	}
	hash, err := enrollmentSHAFile(path, lock.Bytes)
	if err != nil || hash != lock.SHA256 {
		return "", errors.New("voice_enrollment_base_model_unavailable")
	}
	return path, nil
}

// runEnrollment stages immutable normalized samples and invokes the exact
// runner from the currently installed speech package.  `root` is the private
// profile directory, never a caller-supplied workspace path.
func (p *permittedTools) runEnrollment(ctx context.Context, pr enrollmentProfile, job enrollmentJob, root string) (enrollmentResult, error) {
	var zero enrollmentResult
	if p == nil || p.manager == nil || !p.enrollmentAllowed() || pr.ID == "" || pr.Owner != p.trace.Sender || job.ID == "" || job.Revision != pr.Revision || !pathWithin(p.manager.enrollDir(pr.ID), root) || filepath.Clean(root) != p.manager.enrollDir(pr.ID) {
		return zero, errors.New("voice_enrollment_authorization_changed")
	}
	if job.Action != "preview" && job.Action != "train" {
		return zero, errors.New("voice_enrollment_action_invalid")
	}
	store, err := p.manager.packageStore()
	if err != nil {
		return zero, err
	}
	installed, err := store.Installed(ctx, "speech")
	if err != nil || job.Digest != installed.Digest || !installed.Enabled || installed.Removed || store.CheckConfiguration(ctx, "speech") != nil {
		return zero, errors.New("voice_enrollment_speech_plugin_unavailable")
	}
	if err = pluginhost.Authorize(installed.Package, pluginhost.Grant{Account: p.trace.Account, Plugin: installed.Package.ID, Version: installed.Package.Version, Permissions: installed.Permissions}, pluginhost.Invocation{Account: p.trace.Account, Thread: p.trace.Thread, Requester: p.trace.Sender, Purpose: "private voice enrollment", RequestKey: job.ID}, []string{"model.invoke"}); err != nil {
		return zero, errors.New("voice_enrollment_plugin_permission_denied")
	}
	packageDir, err := store.PackageDirectory(installed.Digest)
	if err != nil {
		return zero, err
	}
	manifestProof, err := readEnrollmentFile(filepath.Join(packageDir, "plugin.json"), 1<<20)
	if err != nil || namedSpeechSHA(manifestProof) != job.Digest {
		return zero, errors.New("voice_enrollment_manifest_changed")
	}
	verifiedPackage, err := pluginhost.Verify(packageDir)
	if err != nil || verifiedPackage.ID != installed.Package.ID || verifiedPackage.Version != installed.Package.Version {
		return zero, errors.New("voice_enrollment_package_integrity_failed")
	}
	runner := filepath.Join(packageDir, "runtime", "enroll.py")
	lockPath := filepath.Join(packageDir, "base_models", "enrollment-v2.json")
	if err = regularMusicPath(runner, false); err != nil {
		return zero, errors.New("voice_enrollment_runner_missing")
	}
	var locks enrollmentModelLocks
	lockBytes, err := os.ReadFile(lockPath)
	if err != nil || json.Unmarshal(lockBytes, &locks) != nil || locks.SchemaVersion != 1 || locks.Engine != "GPT-SoVITS" || locks.EngineVersion != "v2" {
		return zero, errors.New("voice_enrollment_base_model_lock_invalid")
	}
	x := p.executionContext()
	manifests, _, err := readProjectManifests(ctx, x.Public, "gpt-sovits", true)
	var project struct {
		Root             string `json:"root"`
		Python           string `json:"python"`
		Models           string `json:"base_models"`
		Packages         string `json:"packages"`
		TrainingPackages string `json:"training_packages"`
	}
	if err != nil || len(manifests) != 1 || json.Unmarshal(manifests[0], &project) != nil || regularMusicPath(project.Root, true) != nil || regularMusicPath(project.Python, false) != nil || regularMusicPath(project.Models, true) != nil {
		return zero, errors.New("voice_enrollment_runtime_unavailable")
	}
	if project.Packages != "" && regularMusicPath(project.Packages, true) != nil {
		return zero, errors.New("voice_enrollment_runtime_unavailable")
	}
	trainingPackages := ""
	if job.Action == "train" {
		trainingPackages = project.TrainingPackages
		if trainingPackages == "" && runtime.GOOS == "windows" {
			// Existing native installations register the shared training overlay
			// separately from their inference-only GPT environment.
			runtimes := filepath.Join(x.Public, "runtimes")
			if regularMusicPath(filepath.Join(runtimes, "windows-rvc-training-package-versions.json"), false) == nil {
				trainingPackages = filepath.Join(runtimes, "windows-rvc-training-packages")
			}
		}
		if trainingPackages != "" && (!pathWithin(x.Public, trainingPackages) || regularMusicPath(trainingPackages, true) != nil) {
			return zero, errors.New("voice_training_packages_invalid")
		}
	}
	gpt, err := enrollmentLockedPath(project.Models, locks.GPT)
	if err != nil {
		return zero, err
	}
	sovits, err := enrollmentLockedPath(project.Models, locks.SoVITS)
	if err != nil {
		return zero, err
	}
	disc, err := enrollmentLockedPath(project.Models, locks.Discriminator)
	if err != nil {
		return zero, err
	}
	jobDir := filepath.Join(root, "jobs", job.ID)
	if !pathWithin(root, jobDir) {
		return zero, errors.New("voice_enrollment_job_path_invalid")
	}
	if err = ensureManaged(jobDir, filepath.Join(jobDir, "samples")); err != nil {
		return zero, err
	}
	samples := make([]map[string]any, 0, len(pr.Samples))
	for _, sample := range pr.Samples {
		if len(sample.SHA) != 64 || strings.Trim(sample.SHA, "0123456789abcdef") != "" || sample.Message == "" || strings.TrimSpace(sample.Transcript) == "" {
			return zero, errors.New("voice_enrollment_sample_invalid")
		}
		source := filepath.Join(root, "samples", sample.SHA+".wav")
		if hash, e := enrollmentSHAFile(source, 0); e != nil || hash != sample.SHA {
			return zero, errors.New("voice_enrollment_sample_changed")
		}
		from, openErr := os.Open(source)
		if openErr != nil {
			return zero, openErr
		}
		rootFS, rootErr := workspacefs.EnsurePath(jobDir)
		if rootErr != nil {
			from.Close()
			return zero, rootErr
		}
		copyErr := publishVerifiedStream(rootFS, filepath.Join("samples", sample.SHA+".wav"), from, fileSize(source), sample.SHA)
		from.Close()
		rootFS.Close()
		if copyErr != nil {
			return zero, copyErr
		}
		entry := map[string]any{"audio": filepath.ToSlash(filepath.Join("samples", sample.SHA+".wav")), "sha256": sample.SHA, "transcript": sample.Transcript, "message_id": sample.Message}
		if sample.SingleSpeakerConfirmed && sample.ConfirmationMessage != "" {
			entry["owner_attestation"] = map[string]string{"kind": "owner_attested_single_speaker", "audio_sha256": sample.SHA, "message_id": sample.Message, "instruction_message_id": sample.ConfirmationMessage}
		}
		samples = append(samples, entry)
	}
	manifest := map[string]any{"protocol_version": 1, "action": job.Action, "job_id": job.ID, "profile_id": pr.ID, "revision": pr.Revision, "samples": samples, "preview_text": job.Text, "project": project.Root, "models": project.Models, "python": project.Python, "device": "cuda", "version": "v2", "base_models": map[string]any{"gpt_path": gpt, "gpt_sha256": locks.GPT.SHA256, "sovits_path": sovits, "sovits_sha256": locks.SoVITS.SHA256, "discriminator_path": disc, "discriminator_sha256": locks.Discriminator.SHA256}}
	if project.Packages != "" {
		manifest["packages"] = project.Packages
	}
	if job.Action == "train" {
		manifest["confirm_train"] = true
		if trainingPackages != "" {
			manifest["training_packages"] = trainingPackages
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return zero, err
	}
	if err = writePrivate(filepath.Join(jobDir, "manifest.json"), encoded); err != nil {
		return zero, err
	}
	cfg, err := p.hostConfig()
	if err != nil {
		return zero, err
	}
	if cfg.Isolation == nil {
		return zero, errors.New("voice_enrollment_sandbox_required")
	}
	cfg.Roots = []string{jobDir}
	cfg.Isolation.GPU = true
	cfg.Isolation.ReadOnly = []string{packageDir, project.Root, project.Models, filepath.Dir(project.Python)}
	if runtime.GOOS == "windows" {
		// The managed interpreter's `.pth` resolves torch and CUDA dependencies
		// from this sibling overlay.  Mounting only the interpreter directory
		// works in a developer shell but leaves LPAC unable to import torch.
		musicPackages := filepath.Join(x.Public, "runtimes", "windows-music-packages")
		if err = regularMusicPath(musicPackages, true); err != nil {
			return zero, errors.New("voice_enrollment_runtime_unavailable")
		}
		// `windows-music-extras` is another managed interpreter overlay (for
		// ffmpeg-python and other small bridge packages), referenced from its
		// .pth.  It is a runtime dependency, not a writable job input.
		musicExtras := filepath.Join(x.Public, "runtimes", "windows-music-extras")
		if err = regularMusicPath(musicExtras, true); err != nil {
			return zero, errors.New("voice_enrollment_runtime_unavailable")
		}
		cfg.Isolation.ReadOnly = append(cfg.Isolation.ReadOnly, musicPackages, musicExtras, filepath.Join(x.Public, "runtimes", "windows-gpt-nltk-data"))
	}
	if project.Packages != "" {
		cfg.Isolation.ReadOnly = append(cfg.Isolation.ReadOnly, project.Packages)
	}
	if runtime.GOOS == "windows" {
		base := filepath.Join(x.Private, "scratch")
		if err = ensureManaged(base); err != nil {
			return zero, err
		}
		scratch, scratchErr := os.MkdirTemp(base, "voice-enrollment-")
		if scratchErr != nil {
			return zero, scratchErr
		}
		defer os.RemoveAll(scratch)
		cfg.Roots = []string{scratch, jobDir}
	}
	if trainingPackages != "" {
		cfg.Isolation.ReadOnly = append(cfg.Isolation.ReadOnly, trainingPackages)
	}
	argv := []string{project.Python, "-X", "utf8", "-B", runner, "--manifest", filepath.Join(jobDir, "manifest.json")}
	var output []byte
	if runtime.GOOS == "windows" {
		output, _, err = p.manager.runWindowsNamedModel(ctx, cfg, jobDir, argv)
	} else {
		output, err = agenthost.RunIsolated(ctx, cfg, jobDir, argv)
	}
	_ = output
	runErr := err
	resultBytes, err := readEnrollmentFile(filepath.Join(jobDir, "result.json"), 128<<10)
	var reported enrollmentWorkerResult
	if err != nil || json.Unmarshal(resultBytes, &reported) != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if runErr != nil {
			return zero, fmt.Errorf("voice_enrollment_process_failed: %w", runErr)
		}
		return zero, errors.New("voice_enrollment_result_invalid")
	}
	if reported.JobID != job.ID || reported.ProfileID != pr.ID || reported.Revision != pr.Revision || reported.WechatSends != 0 {
		return zero, errors.New("voice_enrollment_result_binding_invalid")
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if reported.State == "failed" && strings.TrimSpace(reported.Reason) != "" {
			return zero, errors.New("voice_enrollment_failed_" + safeEnrollmentReason(reported.Reason))
		}
		return zero, errors.New("voice_enrollment_runner_failed")
	}
	if job.Action == "preview" && reported.State != "reference_preview_ready" || job.Action == "train" && reported.State != "trained" {
		return zero, errors.New("voice_enrollment_" + reported.State)
	}
	if job.Action == "train" && reported.SpeakerCheck != "passed" && reported.SpeakerCheck != "owner_attested" {
		return zero, errors.New("voice_enrollment_speaker_verification_required")
	}
	preview, e := enrollmentSHAFile(filepath.Join(jobDir, "preview.wav"), 0)
	if e != nil || preview != reported.PreviewSHA {
		return zero, errors.New("voice_enrollment_preview_unverified")
	}
	previewAudio, e := enrollmentReadAudio(filepath.Join(jobDir, "preview.wav"))
	if e != nil {
		return zero, errors.New("voice_enrollment_preview_audio_invalid")
	}
	if _, _, e = namedSpeechWAVInfo(previewAudio); e != nil {
		return zero, errors.New("voice_enrollment_preview_audio_invalid")
	}
	reference, e := enrollmentSHAFile(filepath.Join(jobDir, "reference.wav"), 0)
	if e != nil || reference != reported.ReferenceSHA || strings.TrimSpace(reported.ReferenceText) == "" {
		return zero, errors.New("voice_enrollment_reference_unverified")
	}
	result := enrollmentResult{Dir: jobDir, ReferenceText: reported.ReferenceText, PreviewSHA: preview, ReferenceSHA: reference, Reason: reported.Reason, SampleSeconds: reported.SampleSeconds, PreviewSeconds: reported.PreviewSeconds}
	if reported.State == "trained" {
		g, e := enrollmentSHAFile(filepath.Join(jobDir, "gpt.ckpt"), 0)
		if e != nil || g != reported.GPTSHA {
			return zero, errors.New("voice_enrollment_gpt_unverified")
		}
		s, e := enrollmentSHAFile(filepath.Join(jobDir, "sovits.pth"), 0)
		if e != nil || s != reported.SoVITSSHA {
			return zero, errors.New("voice_enrollment_sovits_unverified")
		}
		if g == locks.GPT.SHA256 || s == locks.SoVITS.SHA256 {
			return zero, errors.New("voice_enrollment_training_did_not_create_new_weights")
		}
		result.GPTSHA, result.SoVITSSHA = g, s
	}
	return result, nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return info.Size()
}

func enrollmentReadAudio(path string) ([]byte, error) {
	if err := regularMusicPath(path, false); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() < 44 || info.Size() > 25<<20 {
		return nil, errors.New("voice_enrollment_audio_size_invalid")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 25<<20+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, errors.New("voice_enrollment_audio_read_failed")
	}
	return data, nil
}

func safeEnrollmentReason(value string) string {
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return -1
	}, value)
	if value == "" || len(value) > 96 {
		return "worker_failed"
	}
	return value
}
