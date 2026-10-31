package admin

import (
	"encoding/json"
	"net/http"
	"time"
)

// Console actions share the live Manager and the exact Agent media handlers.
// They are authenticated administrator operations, never fabricated incoming
// WeChat messages. No independent manager may race the live delivery journal.
func (s *Server) mediaConsoleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agent/media-send", s.protected(func(w http.ResponseWriter, r *http.Request) {
		access, err := s.consoleGlobalAccess()
		if err != nil {
			fail(w, 403, err.Error())
			return
		}
		var in crossMediaInput
		if !decode(w, r, &in) {
			return
		}
		// Console transfers must specify both ends rather than silently choosing
		// the owner's private chat as a source or destination.
		if in.Source == "" || in.Target == "" {
			fail(w, 400, "需明确指定来源和目标会话")
			return
		}
		if !access.crossAllowed(in.Source, in.Target) {
			fail(w, 400, "来源或目标会话未授权")
			return
		}
		// The converter owns the requested execution deadline; the server's
		// ordinary response timeout must not silently shorten that budget.
		if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && err != http.ErrNotSupported {
			fail(w, 500, "无法设置媒体处理期限")
			return
		}
		access.trace.TraceID = "console-media-" + agentID(in.Key)
		access.trace.OriginTraceID = access.trace.TraceID
		access.trace.Mode = "admin_action"
		raw, _ := json.Marshal(in)
		result, err := access.sendMedia(r.Context(), raw)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(result))
	}))
	mux.HandleFunc("GET /api/agent/media-status/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		access, err := s.consoleGlobalAccess()
		if err != nil {
			fail(w, 403, err.Error())
			return
		}
		result, err := access.crossMediaStatus(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(result))
	}))
}
