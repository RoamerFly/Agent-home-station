package admin

import (
	"strings"
	"testing"
)

func TestConsoleAssetsServeRoundProjectionWithCorrectContentTypes(t *testing.T) {
	f := setup(t)
	for _, item := range []struct{ path, contentType, marker string }{
		{"/assets/rounds.js", "text/javascript", "WechatRounds"},
		{"/assets/save-actions.js", "text/javascript", "WechatSaveActions"},
		{"/assets/console.css", "text/css", ".round-summary"},
		{"/assets/workbench.js", "text/javascript", "WechatWorkbench"},
		{"/assets/workbench.css", "text/css", ".flow-node"},
		{"/", "text/html", `id="log-view"`},
	} {
		status, body, response := f.request("GET", item.path, nil, false, false)
		if status != 200 || !strings.Contains(response.Header.Get("Content-Type"), item.contentType) || !strings.Contains(string(body), item.marker) {
			t.Fatalf("console asset unavailable: %s HTTP %d", item.path, status)
		}
	}
}
