package station

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/websession"
)

func TestLocalAppConfigurationGenericAndSourceOnly(t *testing.T) {
	owner, err := websession.New("synthetic-local-password", "", "synthetic-configuration")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := apphost.New(apphost.Registration{
		Application: apphost.Application{ID: "synthetic-preview", Name: "合成应用", InstallationID: "synthetic-install", Registration: "synthetic"},
		Provider:    &configurationFixtureProvider{},
	}, apphost.Registration{
		Application: apphost.Application{ID: "wechat", Name: "微信", InstallationID: "synthetic-wechat", Registration: "station-native"},
		Provider:    &configurationFixtureProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", owner.Login)
	applicationConfigurationRoutes(owner, mux, registry)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}}
	const path = "/api/local/apps/"
	if code, _ := f.request("GET", path+"synthetic-preview/configuration", nil, false); code != 401 {
		t.Fatalf("anonymous status = %d", code)
	}
	f.login()
	code, wechat := f.request("GET", path+"wechat/configuration", nil, false)
	if code != 200 {
		t.Fatal(code, wechat)
	}
	hasRounds := false
	hasRules := false
	for _, section := range wechat["sections"].([]any) {
		row := section.(map[string]any)
		hasRounds = hasRounds || row["id"] == "rounds" && row["title"] == "执行回合"
		hasRules = hasRules || row["id"] == "rules" && row["title"] == "回复规则"
	}
	if !hasRounds {
		t.Fatal("readable execution process must remain available", wechat)
	}
	if !hasRules {
		t.Fatal("readable reply rules must remain available", wechat)
	}
	code, generic := f.request("GET", path+"synthetic-preview/configuration", nil, false)
	if code != 200 || generic["registered"] != true || len(generic["sections"].([]any)) != 3 {
		t.Fatalf("generic application: %d %#v", code, generic)
	}
	code, douyin := f.request("GET", path+"douyin/configuration", nil, false)
	if code != 200 || douyin["registered"] != false || len(douyin["sections"].([]any)) != 7 {
		t.Fatalf("source-only application: %d %#v", code, douyin)
	}
	if code, _ := f.request("GET", path+"unknown/configuration", nil, false); code != 404 {
		t.Fatalf("unknown application status = %d", code)
	}
}

type configurationFixtureProvider struct{ apphost.Provider }
