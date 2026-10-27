package admin

import (
	"context"
	"strings"
	"testing"
	"wechat-bot/internal/linuxnative"
)

type catalogReadNative struct {
	*fakeNative
	err error
}

func (n *catalogReadNative) Catalog(ctx context.Context, _, _, _, _ string) (linuxnative.CatalogPage, error) {
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded browser catalog request")
	}
	return linuxnative.CatalogPage{Items: []map[string]any{}}, n.err
}

func TestCatalogReturnsEmptyPageAndBoundedTypedFailure(t *testing.T) {
	f := setup(t)
	n := &catalogReadNative{fakeNative: f.native}
	f.manager.native = n
	f.manager.config.GroupRules = []GroupRule{{ID: "room@chatroom", Mode: "mention"}}
	f.login()
	path := "/api/catalog?kind=members&thread=room@chatroom"
	if code, data, _ := f.request("GET", path, nil, false, false); code != 200 || !strings.Contains(string(data), `"items":[]`) {
		t.Fatal("empty page changed into error", code)
	}
	n.err = &linuxnative.CatalogError{Code: "catalog_cursor_invalid"}
	if code, data, _ := f.request("GET", path, nil, false, false); code != 502 || !strings.Contains(string(data), "游标") {
		t.Fatal("query failure cause lost", code)
	}
}
