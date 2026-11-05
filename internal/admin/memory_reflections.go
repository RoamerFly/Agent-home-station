package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"wechat-bot/internal/companion"
)

func reflectionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "找不到这条想法")
	case errors.Is(err, companion.ErrMemoryConflict):
		fail(w, http.StatusConflict, "想法或观察来源已改变，请重新打开后查看")
	case errors.Is(err, companion.ErrInvalidExperienceReflection):
		fail(w, http.StatusBadRequest, "想法内容或分页参数无效")
	default:
		fail(w, http.StatusInternalServerError, "本地记忆暂时不可用")
	}
}

func (s *Server) memoryReflections(w http.ResponseWriter, r *http.Request) {
	scope, err := s.manager.memoryScope(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, "会话不存在")
		return
	}
	q := companion.ExperienceReflectionQuery{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		q.Limit, err = strconv.Atoi(raw)
		if err != nil || q.Limit < 1 {
			fail(w, http.StatusBadRequest, "分页条数无效")
			return
		}
	}
	page, err := s.manager.memory.ListExperienceReflections(r.Context(), scope.ID, q)
	if err != nil {
		reflectionError(w, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func (s *Server) editMemoryReflection(w http.ResponseWriter, r *http.Request) {
	scope, err := s.manager.memoryScope(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, "会话不存在")
		return
	}
	var input struct {
		Action   string `json:"action"`
		Revision int    `json:"expected_revision"`
		Note     string `json:"note"`
		// Feeling stays a pointer so existing clients that only edit an
		// interest note retain the old UpdateExperienceReflection behavior.
		// A present but empty value deliberately clears the recorded feeling.
		Feeling *string `json:"feeling"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Revision < 1 {
		fail(w, http.StatusBadRequest, "请提供当前想法的版本")
		return
	}
	id := r.PathValue("reflection")
	var item companion.ExperienceReflection
	switch input.Action {
	case "correct":
		if input.Feeling == nil {
			item, err = s.manager.memory.UpdateExperienceReflection(r.Context(), scope.ID, id, input.Revision, input.Note)
		} else {
			item, err = s.manager.memory.SetExperienceReflectionContent(r.Context(), scope.ID, id, input.Revision, input.Note, *input.Feeling)
		}
	case "forget":
		item, err = s.manager.memory.ForgetExperienceReflection(r.Context(), scope.ID, id, input.Revision)
	default:
		fail(w, http.StatusBadRequest, "未知的想法操作")
		return
	}
	if err != nil {
		reflectionError(w, err)
		return
	}
	respond(w, http.StatusOK, item)
}
