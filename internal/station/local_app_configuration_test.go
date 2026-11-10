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
