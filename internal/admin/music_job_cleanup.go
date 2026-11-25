package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var musicReviewJobID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// The management page may forget stopped review records. The bridge owns the
// in-memory journal, so editing jobs.json from the web process would be lost.
func (s *Server) musicReviewRequest(r *http.Request, method, path string) ([]byte, int, error) {
	ctx := s.speechPluginTestContext(r)
	authority, _ := ctx.Value(speechAdminTestContextKey{}).(*speechAdminTestAuthority)
	if err := authority.valid(ctx, s.manager); err != nil {
		return nil, 0, err
	}
	release, ok := s.manager.holdWindowsMusic()
	if !ok {
		return nil, 0, errors.New("music_service_not_running")
	}
	defer release()
	endpoint, token, err := s.manager.localMusicPluginConnection(ctx)
	if err != nil {
		return nil, 0, err
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "" || u.RawQuery != "" {
		return nil, 0, errors.New("music_review_requires_local_service")
	}
	data, _, status, err := localMusicHTTP(ctx, method, strings.TrimRight(endpoint, "/")+path, nil, "", token, accountDirectory(s.manager.native.Account()))
	return data, status, err
}

func (s *Server) listMusicReviewJobs(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		fail(w, http.StatusBadRequest, "music_review_query_invalid")
		return
	}
	data, status, err := s.musicReviewRequest(r, "GET", "/jobs?state=needs_review")
	if err != nil || status != http.StatusOK {
		fail(w, http.StatusConflict, "music_review_list_unavailable")
		return
	}
	var result struct {
		Items []struct {
			ID      string  `json:"id"`
			Engine  string  `json:"engine"`
			Phase   string  `json:"phase"`
			Created float64 `json:"created"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if json.Unmarshal(data, &result) != nil || result.Total != len(result.Items) {
		fail(w, http.StatusBadGateway, "music_review_list_invalid")
		return
	}
	respond(w, http.StatusOK, result)
}

func (s *Server) deleteMusicReviewJobs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("job")
	if id != "needs-review" && !musicReviewJobID.MatchString(id) {
		fail(w, http.StatusBadRequest, "music_review_job_invalid")
		return
	}
	data, status, err := s.musicReviewRequest(r, "DELETE", "/jobs/"+id)
	if err != nil {
		fail(w, http.StatusConflict, "music_review_delete_unavailable")
		return
	}
	if status != http.StatusOK {
		fail(w, http.StatusConflict, "music_review_job_not_deletable")
		return
	}
	var result struct {
		Deleted          int `json:"deleted"`
		RemainingReviews int `json:"remaining_needs_review"`
	}
	if json.Unmarshal(data, &result) != nil || result.Deleted < 0 || result.RemainingReviews < 0 {
		fail(w, http.StatusBadGateway, "music_review_delete_result_invalid")
		return
	}
	respond(w, http.StatusOK, result)
}
