package station

import (
	"context"
	"errors"
	"net/http"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/websession"
)

// wechatAppPower is the station-level on/off switch for the WeChat application.
// It only pauses the workers of connections the owner already enabled; it never
// starts a connection the owner has not enabled and never touches the native
// process or the workstation layout.
type wechatAppPower struct {
	App         string `json:"app"`
	Enabled     bool   `json:"enabled"`
	Connections int    `json:"connections"`
	Paused      int    `json:"paused"`
}

// appPowerWorkers returns a stable copy of the loaded workers. Callers persist
// policy per worker, so the station mutex must not be held across that IO.
func (s *stationWechat) appPowerWorkers() []*stationWechatWorker {
	s.mu.Lock()
	defer s.mu.Unlock()
	workers := make([]*stationWechatWorker, 0, len(s.workers))
	for _, worker := range s.workers {
		workers = append(workers, worker)
	}
	return workers
}

func (s *stationWechat) appPower() wechatAppPower {
	view := wechatAppPower{App: "wechat"}
	for _, worker := range s.appPowerWorkers() {
		worker.mu.Lock()
		policy := worker.policy
		worker.mu.Unlock()
		// A connection the owner never enabled is not part of the app switch.
		if !policy.Enabled {
			continue
		}
		view.Connections++
		if policy.Paused {
			view.Paused++
		}
	}
	// With no enabled connection the app is simply not in use; report off only
	// when we actually paused something.
	view.Enabled = view.Connections == 0 || view.Paused < view.Connections
	return view
}

// setAppPower pauses or resumes every enabled WeChat worker and stops a running
// worker when the application is switched off. The caller already proved owner
// authorization through the CSRF-protected route.
func (s *stationWechat) setAppPower(ctx context.Context, enabled bool) (wechatAppPower, error) {
	for _, worker := range s.appPowerWorkers() {
		if err := ctx.Err(); err != nil {
			return s.appPower(), err
		}
		worker.mu.Lock()
		if !worker.policy.Enabled || worker.policy.Paused == !enabled {
			worker.mu.Unlock()
			continue
		}
		next := worker.policy
		next.Paused = !enabled
		err := worker.save(next)
		if err == nil && !enabled {
			// stop reads the run handle, so it stays under the worker mutex.
			err = worker.stop(ctx)
		}
		worker.mu.Unlock()
		if err != nil && !errors.Is(err, apphost.ErrLifecycleConflict) {
			return s.appPower(), err
		}
	}
	return s.appPower(), nil
}

func (s *stationWechat) appPowerRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	view := s.appPower()
	switch r.Method {
	case http.MethodGet:
		websession.Respond(w, http.StatusOK, view)
	case http.MethodPost:
		var in struct {
			Enabled *bool `json:"enabled"`
		}
		if !websession.Decode(w, r, &in) {
			return
		}
		if in.Enabled == nil {
			websession.Fail(w, http.StatusBadRequest, "请明确开启或关闭微信应用")
			return
		}
		next, err := s.setAppPower(r.Context(), *in.Enabled)
		if err != nil {
			logFailure("微信应用开关", err)
			websession.Fail(w, http.StatusServiceUnavailable, failureMessage(err, "微信应用状态未能保存，请稍后重试"))
			return
		}
		websession.Respond(w, http.StatusOK, next)
	default:
		websession.Fail(w, http.StatusMethodNotAllowed, "不支持的请求方式")
	}
}
