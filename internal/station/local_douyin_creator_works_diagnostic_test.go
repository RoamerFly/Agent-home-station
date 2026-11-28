package station

import (
	"context"
	"net/http"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
)

func TestLocalDouyinCreatorWorksReturnsFixedDiagnosticCodes(t *testing.T) {
	f, manager := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	_, view := f.request(http.MethodPost, localDouyinWebRoute,
		map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	_, view = f.request(http.MethodPost, localDouyinWebRoute,
		map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true)
	if view["works_enabled"] != true {
		t.Fatalf("works grant unavailable: %v", view)
	}
	request := map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID,
		"content_id": douyinapp.WebWorksContentID, "field": "account_works", "refresh": true}
	for _, tc := range []struct {
		name   string
		cause  error
		want   error
		code   string
		status int
	}{
		{"list not observed", stationbrowser.ErrCreatorWorksListNotObserved, apphost.ErrObservationDouyinWorksListNotObserved, "douyin_works_list_not_observed", http.StatusGatewayTimeout},
		{"response unverified", stationbrowser.ErrCreatorWorksResponseUnverified, apphost.ErrObservationDouyinWorksResponseUnverified, "douyin_works_response_unverified", http.StatusBadGateway},
		{"creator access rejected", stationbrowser.ErrCreatorWorksAccessRejected, apphost.ErrObservationDouyinWorksAccessRejected, "douyin_works_creator_access_required", http.StatusForbidden},
		{"next unavailable", stationbrowser.ErrCreatorWorksNextUnavailable, apphost.ErrObservationDouyinWorksNextUnavailable, "douyin_works_next_unavailable", http.StatusConflict},
		{"response too large", stationbrowser.ErrCreatorWorksResponseTooLarge, apphost.ErrObservationDouyinWorksResponseTooLarge, "douyin_works_response_too_large", http.StatusRequestEntityTooLarge},
		{"owner unverified", stationbrowser.ErrCreatorWorksOwnerUnverified, apphost.ErrObservationDouyinWorksOwnerUnverified, "douyin_works_owner_unverified", http.StatusUnprocessableEntity},
		{"account changed", stationbrowser.ErrCreatorWorksAccountChanged, apphost.ErrObservationDouyinWorksAccountChanged, "douyin_works_account_changed", http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager.readWorks = func(_ context.Context, _ string, _ stationbrowser.DouyinCreatorWorksRequest,
				_ func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
				return stationbrowser.DouyinCreatorWorksPage{}, tc.cause
			}
			status, body := f.request(http.MethodPost, "/api/local/apps/observation", request, true)
			if status != tc.status || body["code"] != tc.code || body["error"] != tc.want.Error() {
				t.Fatalf("diagnostic = %d %v, want %d %s %q", status, body, tc.status, tc.code, tc.want)
			}
		})
	}
}
