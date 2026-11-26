package station

import (
	"context"
	"net/http"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/websession"
)

// This human-only endpoint reads one saved Douyin draft without invoking the
// creator browser or changing publication state. Agent list/status views stay
// metadata-only, so private draft text is not fed back into a later turn.
func (h *localChatService) douyinPublicationReviewRoutes(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/local/apps/douyin/publications/review", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		query := r.URL.Query()
		ids := query["job_id"]
		if len(query) != 1 || len(ids) != 1 || !artifactID("sha-"+ids[0], "sha-") {
			websession.Fail(w, http.StatusBadRequest, "请提供原发布任务编号")
			return
		}
		jobID := ids[0]
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		stop := context.AfterFunc(h.ctx, cancel)
		defer stop()
		publisher, actor, err := h.publicationHost()
		if err != nil {
			status, message := localPublicationFailure(err)
			websession.Fail(w, status, message)
			return
		}
		review, err := publisher.ReviewForOwner(ctx, actor, "douyin", jobID)
		if err != nil {
			status, message := localPublicationFailure(err)
			websession.Fail(w, status, message)
			return
		}
		// A previous login or account cannot reveal this draft through the
		// current workbench, even when its task ID is known to the same owner.
		current, err := publisher.ResolveTarget(ctx, "douyin", review.Task.Target.ConnectionID)
		if err != nil || current != review.Task.Target {
			websession.Fail(w, http.StatusConflict, "原发布任务的抖音账号已变化，请核对原连接")
			return
		}
		visibility, err := localDouyinPublicationVisibility(review.Options)
		if err != nil || len(review.Artifacts) != 1 {
			websession.Fail(w, http.StatusConflict, "原发布草稿无法核对")
			return
		}
		websession.Respond(w, http.StatusOK, struct {
			Task       apphost.PublicationView       `json:"task"`
			Title      string                        `json:"title"`
			Body       string                        `json:"body"`
			Visibility string                        `json:"visibility"`
			Artifacts  []apphost.PublicationArtifact `json:"artifacts"`
		}{review.Task, review.Title, review.Body, visibility, review.Artifacts})
	}))
}
