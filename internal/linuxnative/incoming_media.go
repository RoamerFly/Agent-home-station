package linuxnative

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// IncomingMediaTransferError describes a terminal native download. Transfer
// counters refer to the native transport, before any verified asset is exposed.
type IncomingMediaTransferError struct {
	state, reason    string
	completed, total int64
}

func (e *IncomingMediaTransferError) Error() string {
	code, _ := e.SafeToolFailure()
	return code
}

func (e *IncomingMediaTransferError) SafeToolFailure() (string, string) {
	switch e.state {
	case "expired":
		return "incoming_media_expired", "微信原生服务确认此媒体版本已过期。"
	case "unsupported":
		return "incoming_media_unsupported", "微信原生服务无法取得此媒体版本。"
	case "cancelled":
		return "incoming_media_cancelled", "微信媒体原件读取已取消。"
	case "needs_review":
		return "incoming_media_needs_review", "微信媒体原件的下载结果需要核对。"
	}
	switch e.reason {
	case "media_integrity_failed", "source_integrity_failed":
		return "incoming_media_integrity_mismatch", "微信媒体原件的完整性校验失败。"
	case "media_processing_failed":
		if e.total > 0 && e.completed == e.total {
			return "incoming_media_native_processing_failed", "微信原生下载进度已达 " + strconv.FormatInt(e.total, 10) + " 字节，但原生处理失败，未取得可读取的原件。"
		}
		return "incoming_media_native_processing_failed", "微信原生媒体处理失败，未取得可读取的原件。"
	case "transport_unavailable", "media_disconnected", "media_worker_stopping", "worker_interrupted":
		return "incoming_media_download_failed", "微信原生媒体传输暂不可用。"
	default:
		return "incoming_media_native_download_failed", "微信原生媒体下载已终止，未取得可读取的原件。"
	}
}

// IncomingMedia obtains an owned message's attachment from the native transfer
// API. It never lets a message choose a local filesystem path or bearer URL.
func (s *Sender) incomingMediaResponse(ctx context.Context, messageID string, max int64, quality string) (*http.Response, error) {
	return s.incomingMediaResponseChecked(ctx, messageID, max, quality, nil)
}

func (s *Sender) incomingMediaResponseChecked(ctx context.Context, messageID string, max int64, quality string, checkAsset func(string) error) (*http.Response, error) {
	if len(messageID) == 0 || len(messageID) > 8192 || max < 1 || max > 512<<20 {
		return nil, errors.New("invalid incoming media request")
	}
	switch quality {
	case "best", "normal", "original", "standard", "encoded":
	default:
		return nil, errors.New("invalid incoming media quality")
	}
	prefix := s.accountPath() + "/messages/" + url.PathEscape(messageID)
	body, _ := json.Marshal(map[string]string{"messageId": messageID, "quality": quality})
	raw, err := s.toolJSON(ctx, "POST", prefix+"/media-transfers", "", bytes.NewReader(body), "application/json")
	if err != nil {
		return nil, err
	}
	type mediaJob struct {
		ID        string `json:"id"`
		State     string `json:"state"`
		AssetID   string `json:"assetId"`
		Account   string `json:"accountId"`
		Kind      string `json:"kind"`
		Reason    string `json:"reason"`
		Code      string `json:"errorCode"`
		Completed int64  `json:"completedBytes"`
		Total     int64  `json:"totalBytes"`
	}
	var job mediaJob
	validJob := func() bool {
		return safeID(job.ID) && (checkAsset == nil || job.Account == s.account && job.Kind == "download")
	}
	if json.Unmarshal(raw, &job) != nil || !validJob() {
		return nil, errors.New("invalid incoming media job")
	}
	jobID, assetID := job.ID, job.AssetID
	for job.State != "completed" && job.State != "succeeded" && job.State != "available" {
		switch job.State {
		case "failed", "cancelled", "needs_review", "expired", "unsupported":
			reason := job.Reason
			if reason == "" {
				reason = job.Code
			}
			return nil, &IncomingMediaTransferError{state: job.State, reason: reason, completed: job.Completed, total: job.Total}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		raw, err = s.toolJSON(ctx, "GET", s.accountPath()+"/jobs/"+url.PathEscape(jobID), "", nil, "")
		if err != nil {
			return nil, err
		}
		job = mediaJob{}
		if err := json.Unmarshal(raw, &job); err != nil || !validJob() || job.ID != jobID || assetID != "" && job.AssetID != assetID {
			return nil, errors.New("invalid media job")
		}
		assetID = job.AssetID
	}
	if !safeID(job.AssetID) {
		return nil, errors.New("media asset missing")
	}
	if checkAsset != nil {
		if err := checkAsset(job.AssetID); err != nil {
			return nil, err
		}
	}
	req, err := s.newRequest(ctx, "GET", s.accountPath()+"/media/"+url.PathEscape(job.AssetID)+"/content", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, errors.New("media_content_unavailable")
	}
	return resp, nil
}

// VerifiedIncomingMedia is the native metadata verified against the original stream.
type VerifiedIncomingMedia struct {
	Bytes   int64
	MIME    string
	SHA256  string
	Variant string
}

type incomingMediaAsset struct {
	ID      string `json:"id"`
	Account string `json:"accountId"`
	Message string `json:"messageId"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Variant string `json:"variant"`
	Actual  string `json:"actualVariant"`
	Bytes   int64  `json:"byteLength"`
	MIME    string `json:"contentType"`
	SHA256  string `json:"sha256"`
}

func (s *Sender) incomingMediaSourceVariants(ctx context.Context, message, kind string) ([]string, error) {
	var alternative string
	switch kind {
	case "file":
		return []string{"original"}, nil
	case "voice":
		return []string{"encoded"}, nil
	case "image":
		alternative = "standard"
	case "video":
		alternative = "normal"
	default:
		return nil, errors.New("unsupported incoming media kind")
	}
	var assets struct {
		Items []incomingMediaAsset `json:"items"`
	}
	if err := s.readJSON(ctx, s.accountPath()+"/messages/"+url.PathEscape(message)+"/media", &assets); err != nil {
		return nil, err
	}
	var variants []string
	for _, candidate := range []string{"original", alternative} {
		for _, asset := range assets.Items {
			if asset.Account == s.account && asset.Message == message && asset.Kind == kind && asset.Variant == candidate &&
				asset.State != "expired" && asset.State != "unsupported" && (asset.State != "available" || asset.Actual == candidate) {
				variants = append(variants, candidate)
				break
			}
		}
	}
	if len(variants) == 0 {
		return nil, errors.New("incoming_media_full_source_unavailable")
	}
	return variants, nil
}

// StreamVerifiedIncomingMedia binds the original to the native message and
// declared asset metadata. The caller publishes the staged output only on success.
func (s *Sender) StreamVerifiedIncomingMedia(ctx context.Context, messageID, kind string, max int64, output io.Writer) (VerifiedIncomingMedia, error) {
	if len(messageID) == 0 || len(messageID) > 8192 || max < 1 || max > 512<<20 || output == nil {
		return VerifiedIncomingMedia{}, errors.New("invalid incoming media request")
	}
	variants, err := s.incomingMediaSourceVariants(ctx, messageID, kind)
	if err != nil {
		return VerifiedIncomingMedia{}, err
	}
	var transfer *IncomingMediaTransferError
	for _, variant := range variants {
		result, readErr := s.streamVerifiedIncomingMedia(ctx, messageID, kind, variant, max, output)
		// A terminal job is reported before any bytes reach output. Another
		// declared full rendition can be read independently without mixing data.
		if kind != "video" || !errors.As(readErr, &transfer) || !transfer.canTryOtherVideoVariant() || ctx.Err() != nil {
			return result, readErr
		}
		err = readErr
	}
	return VerifiedIncomingMedia{}, err
}

func (e *IncomingMediaTransferError) canTryOtherVideoVariant() bool {
	if e.state == "expired" || e.state == "unsupported" {
		return true
	}
	return e.state == "failed" && (e.reason == "media_processing_failed" || e.reason == "media_integrity_failed" || e.reason == "source_integrity_failed")
}

// StreamVerifiedPlayableVoice verifies the separately decoded native voice
// rendition. The encoded original is still acquired by the attachment API.
func (s *Sender) StreamVerifiedPlayableVoice(ctx context.Context, messageID string, max int64, output io.Writer) (VerifiedIncomingMedia, error) {
	if output == nil {
		return VerifiedIncomingMedia{}, errors.New("invalid incoming media request")
	}
	return s.streamVerifiedIncomingMedia(ctx, messageID, "voice", "normal", max, output)
}

func (s *Sender) streamVerifiedIncomingMedia(ctx context.Context, messageID, kind, variant string, max int64, output io.Writer) (VerifiedIncomingMedia, error) {
	var expected VerifiedIncomingMedia
	resp, err := s.incomingMediaResponseChecked(ctx, messageID, max, variant, func(id string) error {
		var asset incomingMediaAsset
		err := s.readJSON(ctx, s.accountPath()+"/media/"+url.PathEscape(id), &asset)
		hash, hashErr := hex.DecodeString(asset.SHA256)
		if err != nil || asset.ID != id || asset.Account != s.account || asset.Message != messageID || asset.Kind != kind ||
			asset.State != "available" || asset.Variant != variant || asset.Actual != variant || asset.Bytes <= 0 || asset.Bytes > max || hashErr != nil || len(hash) != sha256.Size {
			return errors.New("incoming_media_asset_identity_unverified")
		}
		expected = VerifiedIncomingMedia{Bytes: asset.Bytes, MIME: asset.MIME, SHA256: hex.EncodeToString(hash), Variant: variant}
		return nil
	})
	if err != nil {
		return VerifiedIncomingMedia{}, err
	}
	defer resp.Body.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(output, hash), io.LimitReader(resp.Body, expected.Bytes+1))
	if err != nil {
		return VerifiedIncomingMedia{}, err
	}
	if n != expected.Bytes || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return VerifiedIncomingMedia{}, errors.New("incoming_media_integrity_mismatch")
	}
	return expected, nil
}

func (s *Sender) IncomingMedia(ctx context.Context, messageID string, max int64) ([]byte, string, error) {
	resp, err := s.incomingMediaResponse(ctx, messageID, max, "best")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil || len(data) == 0 || int64(len(data)) > max {
		return nil, "", errors.New("incoming_media_exceeds_limit")
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// IncomingVoiceMedia asks the native bridge for its normal/playable voice
// rendition.  Raw WeChat SILK is retained only when that bridge cannot supply
// normal media; callers must report that condition instead of treating it as a
// decodable training or ASR input.
func (s *Sender) IncomingVoiceMedia(ctx context.Context, messageID string, max int64) ([]byte, string, error) {
	resp, err := s.incomingMediaResponse(ctx, messageID, max, "normal")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil || len(data) == 0 || int64(len(data)) > max {
		return nil, "", errors.New("incoming_media_exceeds_limit")
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// IncomingMediaTo streams a native-owned transfer to an exclusively created
// private file. The caller owns removal on failure; partial files are never assets.
func (s *Sender) IncomingMediaTo(ctx context.Context, messageID, path string, max int64) (int64, string, error) {
	resp, err := s.incomingMediaResponse(ctx, messageID, max, "best")
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, "", err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	n, err := io.Copy(f, io.LimitReader(resp.Body, max+1))
	if err == nil && n > 0 && n <= max {
		err = f.Sync()
		if err == nil {
			err = f.Close()
		}
		if err == nil {
			ok = true
			return n, resp.Header.Get("Content-Type"), nil
		}
	}
	if err == nil {
		err = errors.New("incoming_media_size_invalid")
	}
	return n, "", err
}
