package adapter

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/linuxnative"
)

func TestNativeMediaTransferRejectsForeignAndLateResults(t *testing.T) {
	const account, thread, id, object, body = "fixture", "room@chatroom", "msg_one", "12345", "synthetic bytes"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	var active, revokeAtResponse atomic.Bool
	var uploads, lookups atomic.Int32
	active.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/accounts/fixture/uploads":
			uploads.Add(1)
			form, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			part, err := form.NextPart()
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			data, err := io.ReadAll(part)
			if err != nil || string(data) != body || part.FileName() != "fixture.txt" {
				t.Error("unexpected upload bytes", err)
			}
			if revokeAtResponse.Load() {
				active.Store(false)
			}
			fmt.Fprintf(w, "{\"id\":\"fixture-upload\",\"name\":\"fixture.txt\",\"byteLength\":%d,\"sha256\":%q}", len(body), sum)
		case "POST /v1/accounts/fixture/messages/msg_one/channels-share-url":
			lookups.Add(1)
			if revokeAtResponse.Load() {
				active.Store(false)
			}
			fmt.Fprint(w, "{\"accountId\":\"fixture\",\"messageId\":\"msg_one\",\"threadId\":\"room@chatroom\",\"objectId\":\"12345\",\"shareUrl\":\"https://weixin.qq.com/sph/Synthetic12\"}")
		default:
			t.Error("unexpected native request", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: server.URL, Account: account, TokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	file := MediaUpload{Account: "another", Filename: "fixture.txt", MIME: "text/plain", Bytes: int64(len(body)), SHA256: sum}
	stage := func() (string, error) {
		return StageMediaUpload(ctx, native, file, strings.NewReader(body), active.Load)
	}
	if got, err := stage(); got != "" || !errors.Is(err, ErrAttachmentDenied) || uploads.Load() != 0 {
		t.Fatal("foreign upload was attempted", got, err)
	}
	file.Account = account
	if got, err := stage(); got != "fixture-upload" || err != nil {
		t.Fatal("authorized upload could not stage", got, err)
	}
	revokeAtResponse.Store(true)
	if got, err := stage(); got != "" || !errors.Is(err, ErrAttachmentDenied) || uploads.Load() != 2 {
		t.Fatal("late upload escaped revocation", got, err, uploads.Load())
	}
	if _, err := StreamAttachment(ctx, native, AttachmentSource{Account: "another", Message: id, Kind: "image"}, false, 256, io.Discard, active.Load); !errors.Is(err, ErrAttachmentDenied) {
		t.Fatal("another account could open a stream", err)
	}
	subtype := 51
	message := linuxnative.Message{AccountID: account, ThreadID: thread, ID: id, Direction: "incoming", Type: 49, AppMessageType: &subtype}
	source := linuxnative.ChannelsSource{AccountID: account, ThreadID: thread, MessageID: id, ObjectID: object}
	original := AttachmentSource{Account: account, Thread: thread, Message: id, Kind: "video", Origin: "channels", Binding: ChannelsVideoBinding(message, source)}
	check := func(context.Context) error {
		if !active.Load() {
			return ErrAttachmentDenied
		}
		return nil
	}
	read := func() (string, error) {
		return ReadBoundChannelsShortLink(ctx, native, original, message, source, check)
	}
	active.Store(true)
	source.ObjectID = "54321"
	if got, err := read(); got != "" || !errors.Is(err, ErrAttachmentDenied) || lookups.Load() != 0 {
		t.Fatal("another card was resolved", got, err)
	}
	source.ObjectID = object
	if got, err := read(); got != "" || !errors.Is(err, ErrAttachmentDenied) || lookups.Load() != 1 {
		t.Fatal("late short link escaped revocation", got, err)
	}
	active.Store(true)
	revokeAtResponse.Store(false)
	if got, err := read(); got != "https://weixin.qq.com/sph/Synthetic12" || err != nil {
		t.Fatal("authorized short link was unavailable", got, err)
	}
}
