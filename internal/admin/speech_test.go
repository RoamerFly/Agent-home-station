package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/companion"
)

func TestSpeechSettingsNeverExposeSecretsAndScopeYuE(t *testing.T) {
	f := setup(t)
	f.login()
	c := bailian.Defaults()
	c.Key = "synthetic.private.api.key"
	c.YuEToken = "synthetic.yue.token"
	c.YuEEndpoint = "http://127.0.0.1:6188"
	c.YuEEnabled = true
	c.TTSEnabled = true
	c.ASREnabled = true
	if code, _, _ := f.request("POST", "/api/speech", c, true, false); code != 403 {
		t.Fatal("csrf", code)
	}
	code, body, _ := f.request("POST", "/api/speech", c, true, true)
	if code != 200 || bytes.Contains(body, []byte(c.Key)) || bytes.Contains(body, []byte(c.YuEToken)) {
		t.Fatal("credential leak", code)
	}
	code, body, _ = f.request("GET", "/api/speech", nil, false, false)
	if code != 200 || bytes.Contains(body, []byte("synthetic")) {
		t.Fatal("secret returned")
	}
	c.Key = ""
	c.YuEToken = ""
	if code, _, _ := f.request("POST", "/api/speech", c, true, true); code != 200 {
		t.Fatal(code)
	}
	stored := f.manager.speechConfig()
	if stored.Key == "" || stored.YuEToken == "" {
		t.Fatal("blank lost credentials")
	}
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "one"})
	if len(p.yueTools()) != 4 {
		t.Fatal("local tools missing")
	}
	if _, err := p.callYuE(context.Background(), "music_local_status", json.RawMessage(`{"job_id":"foreign-job"}`)); err == nil {
		t.Fatal("foreign job inspected")
	}
	p.trace.Preview = true
	if len(p.yueTools()) != 0 {
		t.Fatal("preview allowed effectful song")
	}
	c.Endpoint = "https://dashscope-intl.aliyuncs.com"
	if code, _, _ := f.request("POST", "/api/speech", c, true, true); code != 400 {
		t.Fatal("old key moved to different endpoint")
	}
}
