package station

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/websession"
)

// localToolGrants records which general-purpose capability groups the owner has
// handed to the companion. The home is deliberately quiet by default: only
// memory retrieval and saved-output reading work without an explicit grant, so
// a fresh installation cannot start processes, write outside its own workspace
// files, or drive the local generators on its own.
type localToolGrants struct {
	Revision  uint64 `json:"revision"`
	Files     bool   `json:"files"`
	Process   bool   `json:"process"`
	Documents bool   `json:"documents"`
	Media     bool   `json:"media"`
}

// defaultLocalToolGrants keeps the capabilities an existing home already had:
// turning them off by default would silently remove work the owner relies on.
// The record exists so the owner can withdraw a group, and a withdrawal is
// effective from the next message.
func defaultLocalToolGrants() localToolGrants {
	return localToolGrants{Files: true, Process: true, Documents: true, Media: true}
}

var errLocalToolGrantsRevision = errors.New("工具授权已更新，请刷新后再保存")

type localToolGrantsStore struct {
	path string
	mu   sync.Mutex
}

func newLocalToolGrantsStore(dir string) *localToolGrantsStore {
	return &localToolGrantsStore{path: filepath.Join(dir, "tool-grants.json")}
}

func (s *localToolGrantsStore) View() localToolGrants {
	if s == nil {
		return defaultLocalToolGrants()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *localToolGrantsStore) readLocked() localToolGrants {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return defaultLocalToolGrants()
	}
	value := defaultLocalToolGrants()
	if json.Unmarshal(raw, &value) != nil {
		return defaultLocalToolGrants()
	}
	return value
}

// Save applies a compare-and-swap on Revision so two open windows cannot
// silently overwrite each other's grants.
func (s *localToolGrantsStore) Save(input localToolGrants) (localToolGrants, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.readLocked()
	if input.Revision != current.Revision {
		return current, errLocalToolGrantsRevision
	}
	next := input
	next.Revision = current.Revision + 1
	raw, err := json.Marshal(next)
	if err != nil {
		return current, err
	}
	return next, hostfile.WritePrivate(s.path, raw)
}

func (h *localChatService) toolGrantsRoutes(owner *websession.Store, mux *http.ServeMux) {
	serve := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			websession.Respond(w, http.StatusOK, h.grants.View())
		case http.MethodPut:
			var in localToolGrants
			if !websession.Decode(w, r, &in) {
				return
			}
			next, err := h.grants.Save(in)
			if err != nil {
				if errors.Is(err, errLocalToolGrantsRevision) {
					websession.Fail(w, http.StatusConflict, err.Error())
					return
				}
				logFailure("工具授权保存", err)
				websession.Fail(w, http.StatusInternalServerError, failureMessage(err, "工具授权未能保存，请稍后重试"))
				return
			}
			websession.Respond(w, http.StatusOK, next)
		default:
			websession.Fail(w, http.StatusMethodNotAllowed, "不支持的请求方式")
		}
	}
	mux.HandleFunc("GET /api/local/tool-grants", owner.Protect(serve))
	mux.HandleFunc("PUT /api/local/tool-grants", owner.Protect(serve))
}
