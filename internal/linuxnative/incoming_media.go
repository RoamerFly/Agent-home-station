package linuxnative

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

// IncomingMedia obtains an owned message's attachment from the native transfer
// API. It never lets a message choose a local filesystem path or bearer URL.
func (s *Sender) IncomingMedia(ctx context.Context, messageID string, max int64) ([]byte, string, error) {
	if len(messageID) == 0 || len(messageID) > 8192 || max < 1 || max > 64<<20 {
		return nil, "", errors.New("invalid incoming media request")
	}
	prefix := s.accountPath() + "/messages/" + url.PathEscape(messageID)
	body, _ := json.Marshal(map[string]string{"messageId": messageID, "quality": "best"})
	raw, err := s.toolJSON(ctx, "POST", prefix+"/media-transfers", "", bytes.NewReader(body), "application/json")
	if err != nil {
		return nil, "", err
	}
	var job struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		AssetID string `json:"assetId"`
	}
	if json.Unmarshal(raw, &job) != nil || !safeID(job.ID) {
		return nil, "", errors.New("invalid incoming media job")
	}
	for job.State != "completed" && job.State != "succeeded" && job.State != "available" {
		if job.State == "failed" || job.State == "cancelled" || job.State == "needs_review" {
			return nil, "", errors.New("incoming media unavailable")
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		raw, err = s.toolJSON(ctx, "GET", s.accountPath()+"/jobs/"+url.PathEscape(job.ID), "", nil, "")
		if err != nil {
			return nil, "", err
		}
		if json.Unmarshal(raw, &job) != nil {
			return nil, "", errors.New("invalid media job")
		}
	}
	if !safeID(job.AssetID) {
		return nil, "", errors.New("media asset missing")
	}
	req, err := s.newRequest(ctx, "GET", s.accountPath()+"/media/"+url.PathEscape(job.AssetID)+"/content", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", errors.New("media content unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil || len(data) == 0 || int64(len(data)) > max {
		return nil, "", errors.New("incoming media exceeds limit")
	}
	return data, resp.Header.Get("Content-Type"), nil
}
