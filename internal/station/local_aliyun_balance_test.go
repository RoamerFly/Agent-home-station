package station

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/websession"
)

type aliyunTestTransport func(*http.Request) (*http.Response, error)

func (f aliyunTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAliyunBalanceProviderRequestAndResponse(t *testing.T) {
	credential := aliyunBillingCredential{ID: "synthetic-billing-id", Secret: "synthetic-billing-secret"}
	response := `{"Code":"200","Success":true,"Data":{"Currency":"CNY","AvailableCashAmount":"5.01","AvailableAmount":"7.30"}}`
	client := &http.Client{Transport: aliyunTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != "https://business.aliyuncs.com/" || r.Header.Get("X-Acs-Action") != "QueryAccountBalance" || r.Header.Get("X-Acs-Version") != "2017-12-14" || r.ContentLength != 0 {
			t.Fatal("unexpected billing request")
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "ACS3-HMAC-SHA256 Credential="+credential.ID+",") || strings.Contains(auth, credential.Secret) || !strings.Contains(auth, ",Signature=") {
			t.Fatal("unexpected signature header")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})}
	got, err := queryAliyunBalance(t.Context(), client, credential)
	if err != nil || got != (aliyunBalance{Cash: "5.01", Available: "7.30", Currency: "CNY"}) {
		t.Fatalf("balance = %+v, %v", got, err)
	}
	response = `{"Code":"NoPermission","Success":false}`
	if _, err := queryAliyunBalance(t.Context(), client, credential); !errors.Is(err, errAliyunPermission) {
		t.Fatalf("permission error = %v", err)
	}
	response = `{"Code":"200","Success":true,"Data":{"Currency":"CNY","AvailableCashAmount":"1.00"}}`
	if _, err := queryAliyunBalance(t.Context(), client, credential); !errors.Is(err, errAliyunUnavailable) {
		t.Fatalf("incomplete balance error = %v", err)
	}
}

func TestAliyunBillingCredentialIsOwnerOnlyAndNeverReturned(t *testing.T) {
	dir := t.TempDir()
	home := &localHomeService{dataDir: dir, identity: fishidentity.New(dir)}
	if _, err := home.identity.InitializeLocal(); err != nil {
		t.Fatal(err)
	}
	s := &localProjectTools{home: home}
	const origin = "http://127.0.0.1:8082"
	owner, err := websession.New("synthetic-admin-password", origin, "fixture_billing")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.aliyunBillingRoutes(owner, mux)
	login := httptest.NewRequest(http.MethodPost, origin+"/api/login", strings.NewReader(`{"password":"synthetic-admin-password"}`))
	login.Header.Set("Origin", origin)
	login.Header.Set("Content-Type", "application/json")
	loginResult := httptest.NewRecorder()
	owner.Login(loginResult, login)
	if loginResult.Code != http.StatusOK {
		t.Fatalf("login = %d", loginResult.Code)
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(loginResult.Body.Bytes(), &session); err != nil || session.CSRF == "" {
		t.Fatal("login did not return CSRF token")
	}
	call := func(method, body string, authenticated bool, wantStatus int, wantBody string) {
		t.Helper()
		request := httptest.NewRequest(method, origin+aliyunBalanceRoute, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", origin)
		if authenticated {
			request.AddCookie(loginResult.Result().Cookies()[0])
			request.Header.Set("X-CSRF-Token", session.CSRF)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != wantStatus || !strings.Contains(response.Body.String(), wantBody) || strings.Contains(response.Body.String(), "synthetic-billing-") {
			t.Fatalf("%s: status = %d, expected status %d and safe response", method, response.Code, wantStatus)
		}
	}
	call("GET", "", false, 401, "")
	call("GET", "", true, 200, `"configured":false`)
	call("POST", "{}", true, 409, "请先保存")
	const credential = `{"access_key_id":"synthetic-billing-id","access_key_secret":"synthetic-billing-secret"}`
	call("PUT", credential, false, 401, "")
	call("PUT", credential, true, 200, `"configured":true`)
	call("GET", "", true, 200, `"configured":true`)
	call("PUT", `{"access_key_id":"synthetic-billing-incomplete"}`, true, 400, "请填写")
	call("GET", "", true, 200, `"configured":true`)
	call("PUT", `{"clear":true}`, true, 200, `"configured":false`)
	call("GET", "", true, 200, `"configured":false`)
}
