package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

type failingMemberNative struct{ *fakeNative }

func (n *failingMemberNative) Catalog(context.Context, string, string, string, string) (linuxnative.CatalogPage, error) {
	return linuxnative.CatalogPage{}, &linuxnative.CatalogError{Code: "catalog_cursor_invalid", HTTPStatus: 400}
}

func TestMemberReadReportsActualFixedCauseToModelAndAudit(t *testing.T) {
	f := setup(t)
	f.manager.native = &failingMemberNative{f.native}
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	defer p.hub.Close()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "member-cursor-error"})
	_, err = p.Call(context.Background(), "wechat_read", json.RawMessage(`{"kind":"members","cursor":"expired"}`))
	if err == nil || !strings.Contains(mcptools.FailureDescription(err), "游标") || strings.Contains(mcptools.FailureDescription(err), "连接失败") {
		t.Fatal("wrong model failure explanation", err)
	}
	found := false
	for _, row := range f.manager.Logs() {
		if row.Event == "tool_call_failed" && row.Tool == "wechat_read" {
			found = true
			if row.ToolCode != "catalog_cursor_invalid" || !strings.Contains(row.Reason, "游标") {
				t.Fatal("failure lost in audit", row)
			}
		}
	}
	if !found {
		t.Fatal("read failure not recorded")
	}
}
