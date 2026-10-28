package linuxnative

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// IncomingMedia obtains an owned message's attachment from the native transfer
// API. It never lets a message choose a local filesystem path or bearer URL.
func (s *Sender) incomingMediaResponse(ctx context.Context, messageID string, max int64) (*http.Response, error) {
	if len(messageID) == 0 || len(messageID) > 8192 || max < 1 || max > 512<<20 {
		return nil, errors.New("invalid incoming media request")
	}
	prefix := s.accountPath() + "/messages/" + url.PathEscape(messageID)
	body, _ := json.Marshal(map[string]string{"messageId": messageID, "quality": "best"})
	raw, err := s.toolJSON(ctx, "POST", prefix+"/media-transfers", "", bytes.NewReader(body), "application/json")
	if err != nil {
		return nil, err
	}
	var job struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		AssetID string `json:"assetId"`
	}
	if json.Unmarshal(raw, &job) != nil || !safeID(job.ID) {
		return nil, errors.New("invalid incoming media job")
	}
	for job.State != "completed" && job.State != "succeeded" && job.State != "available" {
		if job.State == "failed" || job.State == "cancelled" || job.State == "needs_review" {
			return nil, errors.New("incoming media unavailable")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		raw, err = s.toolJSON(ctx, "GET", s.accountPath()+"/jobs/"+url.PathEscape(job.ID), "", nil, "")
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &job) != nil {
			return nil, errors.New("invalid media job")
		}
	}
	if !safeID(job.AssetID) {
		return nil, errors.New("media asset missing")
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

func (s *Sender) IncomingMedia(ctx context.Context, messageID string, max int64) ([]byte, string, error) {
	resp, err := s.incomingMediaResponse(ctx, messageID, max)
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
	resp, err := s.incomingMediaResponse(ctx, messageID, max)
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
