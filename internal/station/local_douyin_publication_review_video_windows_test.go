//go:build windows

package station

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"wechat-bot/internal/companion"
)

const douyinReviewVideoPath = "/api/local/apps/douyin/publications/review/video?job_id="

func douyinReviewVideoDraft(t *testing.T, video []byte) (*localHomeFixture, string, string) {
	t.Helper()
	f, _, _, _ := webPublicationFixture(t)
	_, home := f.request("GET", "/api/local/home", nil, false)
	if err := os.WriteFile(filepath.Join(home["directory"].(string), "works", "preview.mp4"), video, 0600); err != nil {
		t.Fatal(err)
	}
	tools := noticeToolSet(t, f, "douyin-review-video-draft", true)
	registered, err := tools.Call(t.Context(), localArtifactPublishTool, json.RawMessage(`{"path":"works/preview.mp4"}`))
	var artifact struct {
		ID string `json:"artifact_id"`
	}
	if err != nil || json.Unmarshal([]byte(registered), &artifact) != nil || artifact.ID == "" {
		t.Fatalf("synthetic video was not registered: %s %v", registered, err)
	}
	args, _ := json.Marshal(map[string]any{
		"action": "draft", "app_id": "douyin", "connection_id": "web-page-text",
		"title": "合成预览标题", "body": "合成预览正文", "artifact_ids": []string{artifact.ID},
		"platform_options": map[string]any{"visibility": "private"},
	})
	created, err := tools.Call(t.Context(), localPublicationToolName, args)
	var draft struct {
		JobID string `json:"job_id"`
	}
	if err != nil || json.Unmarshal([]byte(created), &draft) != nil || draft.JobID == "" {
		t.Fatalf("synthetic Douyin draft was not created: %s %v", created, err)
	}
	return f, draft.JobID, artifact.ID
}

func douyinReviewVideoRequest(t *testing.T, f *localHomeFixture, jobID, rangeHeader string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.server.URL+douyinReviewVideoPath+jobID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", f.server.URL)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	response, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, data
}

func TestDouyinPublicationReviewVideoStreamsVerifiedArtifactAndRange(t *testing.T) {
	video := syntheticDouyinAudioMedia(t, true)
	f, job, artifact := douyinReviewVideoDraft(t, video)
	if status, headers, data := douyinReviewVideoRequest(t, f, job, ""); status != http.StatusOK ||
		!bytes.Equal(data, video) || headers.Get("Content-Type") != "video/mp4" ||
		headers.Get("Content-Disposition") != "inline" || headers.Get("Cache-Control") != "no-store" ||
		headers.Get("X-Content-Type-Options") != "nosniff" || headers.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("owner video preview differs from registered bytes: status=%d headers=%v bytes=%d", status, headers, len(data))
	}
	if status, headers, data := douyinReviewVideoRequest(t, f, job, "bytes=7-30"); status != http.StatusPartialContent ||
		!bytes.Equal(data, video[7:31]) || headers.Get("Content-Range") != "bytes 7-30/"+strconv.Itoa(len(video)) ||
		headers.Get("Content-Type") != "video/mp4" {
		t.Fatalf("owner video range differs: status=%d headers=%v bytes=%d", status, headers, len(data))
	}
	if status, _, _ := douyinReviewVideoRequest(t, f, "invalid", ""); status != http.StatusBadRequest {
		t.Fatalf("invalid task accepted: %d", status)
	}
	if status, _, _ := douyinReviewVideoRequest(t, f, strings.Repeat("a", 64), ""); status != http.StatusNotFound {
		t.Fatalf("unknown task accepted: %d", status)
	}
	jar := f.client.Jar
	f.client.Jar = nil
	status, _, _ := douyinReviewVideoRequest(t, f, job, "")
	f.client.Jar = jar
	if status != http.StatusUnauthorized {
		t.Fatalf("owner session bypassed: %d", status)
	}
	_, web := f.request("GET", localDouyinWebRoute, nil, false)
	status, web = f.request("POST", localDouyinWebRoute, map[string]any{
		"action": "begin_creator", "revision": web["revision"], "approve_profile": true,
	}, true)
	if status != http.StatusOK {
		t.Fatalf("synthetic account transition: %d %v", status, web)
	}
	status, web = f.request("POST", localDouyinWebRoute, map[string]any{
		"action": "confirm", "revision": web["revision"], "attempt_id": web["attempt_id"],
	}, true)
	if status != http.StatusOK {
		t.Fatalf("synthetic account confirmation: %d %v", status, web)
	}
	if status, _, _ := douyinReviewVideoRequest(t, f, job, ""); status != http.StatusConflict {
		t.Fatalf("previous account preview remained available: %d artifact=%s", status, artifact)
	}
}

func TestDouyinPublicationReviewVideoRejectsForgottenSourceAndInvalidContainer(t *testing.T) {
	video := syntheticDouyinAudioMedia(t, false)
	f, job, _ := douyinReviewVideoDraft(t, video)
	store, in, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(companion.ScopeID(in.Channel, in.Account, in.Thread)); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := douyinReviewVideoRequest(t, f, job, ""); status != http.StatusNotFound {
		t.Fatalf("forgotten source preview remained available: %d", status)
	}
	invalid, invalidJob, _ := douyinReviewVideoDraft(t, []byte("synthetic-invalid-mp4-container"))
	if status, headers, _ := douyinReviewVideoRequest(t, invalid, invalidJob, ""); status != http.StatusUnsupportedMediaType || strings.HasPrefix(headers.Get("Content-Type"), "video/") {
		t.Fatalf("invalid container was served as video: %d %v", status, headers)
	}
}
