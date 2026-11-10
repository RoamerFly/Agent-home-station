//go:build windows

package stationbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func syntheticCreatorWork(t *testing.T) (CreatorPublicationQuery, map[string]any) {
	t.Helper()
	created := time.Now().Add(-time.Hour).UTC()
	q := CreatorPublicationQuery{Title: "合成标题", Body: "合成创作", Visibility: "private", CreatedAt: created.Format(time.RFC3339Nano)}
	w := map[string]any{"aweme_id": "7391234567890123456", "item_id": 7391234567890123000,
		"item_title": q.Title, "caption": q.Body, "desc": q.Title + " " + q.Body, "create_time": created.Add(time.Minute).Unix(),
		"status": map[string]any{"aweme_id": "7391234567890123456", "is_private": true, "private_status": 1, "in_reviewing": false, "is_delete": false, "is_prohibited": false, "self_see": true}}
	return q, w
}

func creatorWorkList(works ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"status_code": 0, "aweme_list": works})
	return b
}

func TestCreatorManageRecoveryRequiresExactWorkAndPrivacy(t *testing.T) {
	q, w := syntheticCreatorWork(t)
	if got := creatorManageReceipt(creatorWorkList(w), &q, ""); !got.Published || got.RemoteID != "7391234567890123456" {
		t.Fatalf("private work not identified: %#v", got)
	}
	for _, field := range []string{"item_title", "caption", "desc", "create_time", "status", "aweme_id"} {
		t.Run(field, func(t *testing.T) {
			_, changed := syntheticCreatorWork(t)
			switch field {
			case "create_time":
				changed[field] = int64(1)
			case "status":
				changed[field] = map[string]any{"aweme_id": w["aweme_id"], "is_private": false, "private_status": 0, "in_reviewing": false, "is_delete": false, "is_prohibited": false}
			default:
				changed[field] = "other"
			}
			if got := creatorManageReceipt(creatorWorkList(changed), &q, ""); got.RemoteID != "" {
				t.Fatalf("mismatched work accepted: %#v", got)
			}
		})
	}
	_, duplicate := syntheticCreatorWork(t)
	duplicate["aweme_id"] = "7391234567890123457"
	duplicate["status"].(map[string]any)["aweme_id"] = duplicate["aweme_id"]
	if got := creatorManageReceipt(creatorWorkList(w, duplicate), &q, ""); got.RemoteID != "" {
		t.Fatal("ambiguous caption accepted")
	}
	if got := creatorManageReceipt(creatorWorkList(w), nil, "7391234567890123457"); got.RemoteID != "" {
		t.Fatal("wrong known work accepted")
	}
	w["status"].(map[string]any)["in_reviewing"] = true
	if got := creatorManageReceipt(creatorWorkList(w), nil, "7391234567890123456"); got.Stage != "accepted" || got.Published {
		t.Fatalf("moderation treated as published: %#v", got)
	}
}

func TestLocalCreatorManageRecoveryDoesNotUploadOrPublish(t *testing.T) {
	if !Available() {
		t.Skip("Edge unavailable")
	}
	q, w := syntheticCreatorWork(t)
	posts := new(atomic.Int32)
	s := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || strings.Contains(r.URL.Path, "create") {
			posts.Add(1)
		}
		switch r.URL.Path {
		case "/creator-micro/content/manage":
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = rw.Write([]byte(`<p>作品管理</p><script>fetch('/janus/douyin/creator/pc/work_list')</script>`))
		case "/janus/douyin/creator/pc/work_list":
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write(creatorWorkList(w))
		default:
			http.NotFound(rw, r)
		}
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	got, err := runDouyinCreator(ctx, filepath.Join(t.TempDir(), "profile"), s.URL+"/creator-micro/content/manage", CreatorPublicationRequest{Submitted: true, Query: &q}, func() error { return nil }, true)
	if err != nil || !got.Published || got.RemoteID != "7391234567890123456" || posts.Load() != 0 {
		t.Fatalf("read-only recovery: %#v err=%v writes=%d", got, err, posts.Load())
	}
}

func TestCreatorWorksPaginationUsesOwnedManagementPage(t *testing.T) {
	if !Available() {
		t.Skip("Edge unavailable")
	}
	_, first := syntheticCreatorWork(t)
	first["author"] = map[string]string{"uid": "123456789012345678"}
	_, next := syntheticCreatorWork(t)
	next["aweme_id"] = "7391234567890123457"
	next["status"].(map[string]any)["aweme_id"] = next["aweme_id"]
	next["author"] = first["author"]
	var changes atomic.Int32
	var wrongAccount atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			changes.Add(1)
		}
		switch r.URL.Path {
		case "/creator-micro/content/manage":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<button aria-label="下一页" onclick="fetch('/janus/douyin/creator/pc/work_list?page=2')">下一页</button><script>fetch('/janus/douyin/creator/pc/work_list?page=1')</script>`))
		case "/janus/douyin/creator/pc/work_list":
			item := first
			more := r.URL.Query().Get("page") == "1"
			if !more {
				item = next
				if wrongAccount.Load() {
					item = map[string]any{}
					for key, value := range next {
						item[key] = value
					}
					item["author"] = map[string]string{"uid": "900"}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "has_more": more, "aweme_list": []any{item}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	read := func(input DouyinCreatorWorksRequest) (DouyinCreatorWorksPage, error) {
		return readDouyinCreatorWorks(t.Context(), filepath.Join(t.TempDir(), "profile"), server.URL+"/creator-micro/content/manage", input, func() error { return nil }, true)
	}
	page, err := read(DouyinCreatorWorksRequest{})
	if err != nil || !page.HasMore || len(page.Works) != 1 || page.Works[0].ID != first["aweme_id"] {
		t.Fatalf("first page: %+v %v", page, err)
	}
	continuation := DouyinCreatorWorksRequest{Page: 1, PreviousAccount: page.Account, PreviousPageKey: page.PageKey}
	page, err = read(continuation)
	if err != nil || page.HasMore || len(page.Works) != 1 || page.Works[0].ID != next["aweme_id"] {
		t.Fatalf("second page: %+v %v", page, err)
	}
	wrongAccount.Store(true)
	if _, err := read(continuation); !errors.Is(err, ErrCreatorWorksChanged) {
		t.Fatalf("account switch accepted: %v", err)
	}
	if changes.Load() != 0 {
		t.Fatal("read-only management sent a write")
	}
}
