package station

import (
	"context"
	"errors"
	"testing"

	"wechat-bot/internal/apphost"
)

type catalogProvider struct{ connections []apphost.Connection }

func (p catalogProvider) Connections(context.Context) ([]apphost.Connection, error) {
	return p.connections, nil
}

func (p catalogProvider) Inspect(context.Context, string) (apphost.ConnectionStatus, error) {
	return apphost.ConnectionStatus{}, nil
}

func catalogRegistry(t *testing.T, ids ...string) apphost.Registry {
	t.Helper()
	registrations := make([]apphost.Registration, 0, len(ids))
	for _, id := range ids {
		registrations = append(registrations, apphost.Registration{
			Application: apphost.Application{ID: id, Name: id, InstallationID: id + "-install", Registration: id + "-reg"},
			Provider:    catalogProvider{},
		})
	}
	registry, err := apphost.New(registrations...)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return registry
}

func TestHomeCatalogCarriesNamesAndStateOnly(t *testing.T) {
	registry := catalogRegistry(t, "wechat", "douyin")
	service := &localChatService{
		applications: registry,
		wechat: &stationWechat{workers: map[string]*stationWechatWorker{
			"one": {dir: t.TempDir(), policy: stationWechatPolicy{Enabled: true}},
		}},
	}
	catalog := service.homeCatalog()
	if catalog == nil || catalog["count"] != 2 || catalog["kind"] != "home_apps" {
		t.Fatalf("catalog = %+v", catalog)
	}
	items, _ := catalog["items"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	wechat := items[0]
	for _, item := range items {
		if item["id"] == "wechat" {
			wechat = item
		}
	}
	if wechat["id"] != "wechat" || wechat["state"] != "on" || wechat["connections"] != 1 {
		t.Fatalf("wechat entry = %+v", wechat)
	}
	if _, ok := wechat["hint"]; !ok {
		t.Fatalf("known application lost its hint: %+v", wechat)
	}
	// A switched-off application stays in the catalog but must not look usable.
	if _, err := service.wechat.setAppPower(context.Background(), false); err != nil {
		t.Fatalf("power off: %v", err)
	}
	for _, item := range service.homeCatalog()["items"].([]map[string]any) {
		if item["id"] == "wechat" && item["state"] != "off" {
			t.Fatalf("powered-off application reported as %v", item["state"])
		}
	}
}

func TestHomeCatalogIsAbsentWithoutApplications(t *testing.T) {
	service := &localChatService{applications: catalogRegistry(t)}
	if catalog := service.homeCatalog(); catalog != nil {
		t.Fatalf("empty home announced applications: %+v", catalog)
	}
	if catalog := (*localChatService)(nil).homeCatalog(); catalog != nil {
		t.Fatalf("nil home produced a catalog: %+v", catalog)
	}
}

func TestWechatAppPowerStates(t *testing.T) {
	cases := []struct {
		power wechatAppPower
		state string
		off   bool
	}{
		{wechatAppPower{}, "not_connected", false},
		{wechatAppPower{Connections: 2}, "on", false},
		{wechatAppPower{Connections: 2, Paused: 2}, "off", true},
	}
	for _, c := range cases {
		if got := c.power.state(); got != c.state {
			t.Fatalf("state(%+v) = %s, want %s", c.power, got, c.state)
		}
		if got := c.power.off(); got != c.off {
			t.Fatalf("off(%+v) = %v, want %v", c.power, got, c.off)
		}
	}
}

func TestLocalToolGrantsDefaultAndRevision(t *testing.T) {
	store := newLocalToolGrantsStore(t.TempDir())
	initial := store.View()
	if !initial.Files || !initial.Process || !initial.Documents || !initial.Media {
		t.Fatalf("default grants = %+v", initial)
	}
	next, err := store.Save(localToolGrants{Revision: initial.Revision, Files: true, Documents: true, Media: true})
	if err != nil || next.Process || next.Revision != initial.Revision+1 {
		t.Fatalf("save = %+v err=%v", next, err)
	}
	if _, err = store.Save(localToolGrants{Revision: initial.Revision, Media: true}); !errors.Is(err, errLocalToolGrantsRevision) {
		t.Fatalf("stale revision was accepted: %v", err)
	}
	if view := store.View(); view.Process || !view.Media {
		t.Fatalf("persisted grants = %+v", view)
	}
}
