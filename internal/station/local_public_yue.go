package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"wechat-bot/internal/comfyui"
	"wechat-bot/internal/mediadecode"
	"wechat-bot/internal/mediaspec"
	"wechat-bot/internal/stationyue"
)

type publicYuEProduct struct {
	FLAC     comfyui.FLAC
	Evidence mediadecode.FLACEvidence
	Duration stationyue.DurationCheck
}

// submitPublicYuE is the workstation's generation operation. The caller owns
// the job ID, admission, durable task record and delivery. No application
// message or sender is passed to the music backend.
func (s *localProjectTools) submitPublicYuE(ctx context.Context, fish, jobID string, request stationyue.Request, reference []byte) (localYuEDeployment, localYuESubmission, error) {
	var submission localYuESubmission
	if s == nil || s.yue == nil {
		return localYuEDeployment{}, submission, errors.New("本机 YuE 后端不可用")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return localYuEDeployment{}, submission, err
	}
	if _, err = installedLocalProject(ctx, host, localProjectOperation{pkg: localYuEPackage, capability: localYuECapability}); err != nil {
		return localYuEDeployment{}, submission, err
	}
	deploy, err := s.yue.Info(ctx, fish)
	if err != nil || deploy.Binding == "" {
		return localYuEDeployment{}, submission, errors.New("请在工作站核对本机 YuE 部署")
	}
	ref, err := comfyui.NewJobRef(jobID, localYuESaveNode)
	if err != nil {
		return deploy, submission, err
	}
	if request.Mode == stationyue.ModeCover {
		if len(reference) == 0 || len(reference) > 25<<20 {
			return deploy, submission, errors.New("YuE 翻唱需要最多 25 MiB 的 PCM16 WAV 参考音频")
		}
		if _, _, err = mediaspec.PCM16WAVInfo(reference); err != nil {
			return deploy, submission, errors.New("YuE 参考音频不是有效 PCM16 WAV")
		}
		request.ReferenceBasename, err = comfyui.ReferenceWAVName(ref)
		if err != nil {
			return deploy, submission, err
		}
		hash := sha256.Sum256(reference)
		request.ReferenceSHA256 = hex.EncodeToString(hash[:])
	} else if len(reference) != 0 || request.ReferenceBasename != "" || request.ReferenceSHA256 != "" {
		return deploy, submission, errors.New("YuE 原创请求不能包含参考音频")
	}
	err = s.yue.Use(ctx, fish, deploy.Binding, true, func(client localComfyAudioClient) error {
		prepared, receipt, err := preparePublicYuE(ctx, client, deploy, request, ref)
		if err != nil {
			return err
		}
		submission = receipt
		if len(reference) != 0 {
			name, err := client.UploadWAV(ctx, ref, reference)
			if err != nil || name != request.ReferenceBasename {
				return errors.New("YuE 参考音频未获后端确认")
			}
		}
		return client.Submit(ctx, prepared)
	})
	return deploy, submission, err
}

// publicYuEStatus reads only the exact workstation job selected by the
// caller. A completed result is fully decoded and checked before release.
func (s *localProjectTools) publicYuEStatus(ctx context.Context, fish, binding, jobID string, submission localYuESubmission) (comfyui.AudioStatus, publicYuEProduct, error) {
	var status comfyui.AudioStatus
	var product publicYuEProduct
	if s == nil || s.yue == nil {
		return status, product, errors.New("本机 YuE 后端不可用")
	}
	host, err := s.forFish(fish)
	if err != nil {
		return status, product, err
	}
	if _, err = installedLocalProject(ctx, host, localProjectOperation{pkg: localYuEPackage, capability: localYuECapability}); err != nil {
		return status, product, err
	}
	deploy, err := s.yue.Info(ctx, fish)
	if err != nil || binding == "" || deploy.Binding != binding {
		return status, product, errors.New("YuE 部署已变化")
	}
	ref, err := comfyui.NewJobRef(jobID, localYuESaveNode)
	if err != nil {
		return status, product, err
	}
	err = s.yue.Use(ctx, fish, binding, false, func(client localComfyAudioClient) error {
		status, err = client.AudioStatus(ctx, ref)
		if err != nil || status.State != comfyui.Completed {
			return err
		}
		if status.AudioCount != 1 {
			return errors.New("YuE 成品数量无效")
		}
		product.FLAC, err = client.FetchFLAC(ctx, ref)
		if err != nil {
			return err
		}
		product.Evidence, product.Duration, err = verifyPublicYuEFLAC(ctx, s.home.dataDir, deploy, submission, product.FLAC)
		return err
	})
	return status, product, err
}

func verifyPublicYuEFLAC(ctx context.Context, dataDir string, deploy localYuEDeployment, submission localYuESubmission, flac comfyui.FLAC) (mediadecode.FLACEvidence, stationyue.DurationCheck, error) {
	var empty mediadecode.FLACEvidence
	var noDuration stationyue.DurationCheck
	if deploy.FFmpegPath == "" || flac.SHA256 == "" || len(flac.Data) < 4 || int64(len(flac.Data)) > localYuEMaxInputBytes {
		return empty, noDuration, errors.New("YuE 成品 FLAC 无效")
	}
	root := filepath.Join(dataDir, "yue-output-stage")
	if err := os.MkdirAll(root, 0700); err != nil {
		return empty, noDuration, err
	}
	file, err := os.CreateTemp(root, "verify-*.flac")
	if err != nil {
		return empty, noDuration, err
	}
	path := file.Name()
	defer os.Remove(path)
	_, writeErr := file.Write(flac.Data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	writeErr = errors.Join(writeErr, file.Close())
	if writeErr != nil {
		return empty, noDuration, writeErr
	}
	return verifyYuEFLACAtPath(ctx, path, deploy, submission, flac.SHA256)
}

func verifyYuEFLACAtPath(ctx context.Context, path string, deploy localYuEDeployment, submission localYuESubmission, expectedSHA256 string) (mediadecode.FLACEvidence, stationyue.DurationCheck, error) {
	var empty mediadecode.FLACEvidence
	var noDuration stationyue.DurationCheck
	evidence, err := mediadecode.VerifyFLAC(ctx, path, mediadecode.FLACVerificationOptions{
		FFmpegPath: deploy.FFmpegPath, MaxInputBytes: localYuEMaxInputBytes,
		MaxDecodedPCMBytes: localYuEDecodedLimit(submission.Effective.DurationSeconds),
		MaxSampleRate:      localYuEMaxSampleRate, MaxChannels: localYuEMaxChannels,
	})
	if err != nil || evidence.SHA256 != expectedSHA256 {
		return empty, noDuration, errors.New("YuE 成品 FLAC 未通过完整性校验")
	}
	precision := 1 / float64(evidence.SampleRate)
	duration, err := stationyue.ValidateDuration(evidence.Duration.Seconds(), submission.Effective.DurationSeconds, submission.Effective.MinDurationSeconds, &precision)
	if err != nil || !duration.Accepted {
		return empty, noDuration, errors.New("YuE 成品时长与已提交配方不一致")
	}
	return evidence, duration, nil
}
