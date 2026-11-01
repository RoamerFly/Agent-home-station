package bailian

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestASRNoRecognizableWordsIsDistinctFromProviderFailure(t *testing.T) {
	for _, tc := range []struct {
		body     string
		noSpeech bool
	}{
		{`{"code":"CLIENT_ERROR","message":"ASR_RESPONSE_HAVE_NO_WORDS"}`, true},
		{`{"code":"ASR_RESPONSE_HAVE_NO_WORDS"}`, true},
		{`{"code":"InvalidApiKey","message":"private credential detail"}`, false},
		{`{"code":"CLIENT_ERROR","message":"ASR_RESPONSE_HAVE_NO_WORDS plus private raw details"}`, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400); w.Write([]byte(tc.body)) }))
		c := New(Defaults())
		c.Config.Endpoint = server.URL
		c.Config.Key = "synthetic.private.key"
		_, err := c.Transcribe(context.Background(), []byte("RIFF-synthetic"))
		server.Close()
		if err == nil || IsNoSpeech(err) != tc.noSpeech || strings.Contains(err.Error(), "private credential detail") {
			t.Fatal(err, tc.noSpeech)
		}
	}
}

func TestASRContractAndPrivateKey(t *testing.T) {
	count := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Authorization") != "Bearer synthetic.private.key" {
			t.Error("key truncated")
		}
		var body struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != Defaults().ASRModel || r.URL.Path != "/api/v1/services/aigc/multimodal-generation/generation" {
			t.Error("ASR request changed", body, r.URL.Path)
		}
		w.Write([]byte(`{"output":{"output":{"text":"你好小鲸鱼"}}}`))
	}))
	defer server.Close()
	cfg := Defaults()
	cfg.Endpoint = server.URL
	cfg.Key = "synthetic.private.key"
	c := New(cfg)
	c.HTTP.Transport = server.Client().Transport
	if text, e := c.Transcribe(context.Background(), []byte("RIFF-test")); e != nil || text != "你好小鲸鱼" {
		t.Fatal(text, e)
	}
	if count != 1 {
		t.Fatal(count)
	}
}
