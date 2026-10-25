package admin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/linuxnative"
)

type fakeTools struct {
	*fakeNative
	submits atomic.Int32
	fail    bool
}

func (*fakeTools) Capabilities(context.Context) (linuxnative.Capabilities, error) {
	return linuxnative.Capabilities{MediaKinds: []string{"image", "file", "video", "voice"}, MediaSendMaxBytes: map[string]int64{"image": 1024, "file": 2048, "video": 1024, "voice": 1024}}, nil
}
func (*fakeTools) Upload(_ context.Context, name, mime string, r io.Reader, size int64) (linuxnative.UploadSource, error) {
	body, err := io.ReadAll(r)
	if err != nil || int64(len(body)) != size {
		return linuxnative.UploadSource{}, errors.New("bad upload")
	}
	return linuxnative.UploadSource{ID: "source-one", Filename: name, ContentType: mime, ByteLength: size}, nil
}
func (f *fakeTools) Submit(_ context.Context, r linuxnative.ToolSend) (linuxnative.ToolStatus, error) {
	f.submits.Add(1)
	if f.fail {
		return linuxnative.ToolStatus{}, errors.New("lost response")
	}
	return linuxnative.ToolStatus{ID: "send-one", State: "queued", Kind: r.Kind, ThreadID: r.ThreadID}, nil
}
func (*fakeTools) LookupSend(context.Context, string) (linuxnative.ToolStatus, error) {
	return linuxnative.ToolStatus{ID: "send-one", State: "accepted", ThreadID: "filehelper", ServerID: "18446744073709551615"}, nil
}
func (f *fakeTools) ToolSendStatus(ctx context.Context, id string) (linuxnative.ToolStatus, error) {
	return f.LookupSend(ctx, id)
}
func (f *webFixture) upload(kind string, payload []byte, csrf bool) (int, []byte) {
	f.t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("file", "test.png")
	part.Write(payload)
	form.WriteField("kind", kind)
	form.Close()
	req, _ := http.NewRequest("POST", f.server.URL+"/api/tools/upload", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Origin", f.server.URL)
	if csrf {
		req.Header.Set("X-CSRF-Token", f.csrf)
	}
	response, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	b, _ := io.ReadAll(response.Body)
	return response.StatusCode, b
}
func TestToolsStagingDoesNotSendAndScopesSources(t *testing.T) {
	f := setup(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	if code, _, _ := f.request("GET", "/api/tools/state", nil, false, false); code != 401 {
		t.Fatal("missing auth")
	}
	f.login()
	if code, _ := f.upload("image", []byte("image"), false); code != 403 {
		t.Fatal("upload missing CSRF")
	}
	if code, _ := f.upload("image", []byte("image"), true); code != 201 {
		t.Fatal("staging failed", code)
	}
	if native.submits.Load() != 0 {
		t.Fatal("upload sent a message")
	}
	request := linuxnative.ToolSend{ThreadID: "other", Kind: "image", SourceID: "source-one", IdempotencyKey: "once"}
	if code, _, _ := f.request("POST", "/api/tools/send", request, true, true); code != 403 {
		t.Fatal("unapproved destination accepted")
	}
	request.ThreadID = "filehelper"
	request.Kind = "voice"
	if code, _, _ := f.request("POST", "/api/tools/send", request, true, true); code != 400 {
		t.Fatal("source kind could be substituted")
	}
	request.Kind = "image"
	for i := 0; i < 2; i++ {
		if code, _, _ := f.request("POST", "/api/tools/send", request, true, true); code != 202 {
			t.Fatal("send failed", code)
		}
	}
	if native.submits.Load() != 1 {
		t.Fatal("duplicate operation was sent twice")
	}
	request.Text = "different body"
	if code, _, _ := f.request("POST", "/api/tools/send", request, true, true); code != 409 {
		t.Fatal("key conflict accepted")
	}
	if code, _ := f.upload("image", bytes.Repeat([]byte("x"), 1025), true); code != 400 {
		t.Fatal("upstream bound ignored")
	}
	if code, _, _ := f.request("GET", "/api/tools/sends/unrelated", nil, false, false); code != 404 {
		t.Fatal("unowned task exposed")
	}
}
func TestUnknownSendSurvivesRestartAndQueriesSameKey(t *testing.T) {
	f := setup(t)
	native := &fakeTools{fakeNative: f.native, fail: true}
	f.manager.native = native
	f.login()
	input := linuxnative.ToolSend{ThreadID: "filehelper", Kind: "text", Text: "once", IdempotencyKey: "uncertain-key"}
	code, data, _ := f.request("POST", "/api/tools/send", input, true, true)
	if code != 202 || !bytes.Contains(data, []byte("unknown")) {
		t.Fatal("uncertainty lost", code, string(data))
	}
	reloaded, err := newToolStore(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.tools = reloaded
	if code, _, _ := f.request("POST", "/api/tools/send", input, true, true); code != 202 {
		t.Fatal("same-key replay failed")
	}
	if native.submits.Load() != 1 {
		t.Fatal("restart repeated uncertain send")
	}
	code, data, _ = f.request("GET", "/api/tools/lookup?key=uncertain-key", nil, false, false)
	if code != 200 || !bytes.Contains(data, []byte("18446744073709551615")) {
		t.Fatal("lookup lost exact remote ID")
	}
	ledger, err := os.ReadFile(f.dir + "/tool-state.json")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ledger, []byte("once")) {
		t.Fatal("chat body persisted in task ledger")
	}
}
