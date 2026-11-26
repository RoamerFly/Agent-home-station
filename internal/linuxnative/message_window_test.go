package linuxnative

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func windowMessage(id string, minute int) Message {
	return Message{ID: id, AccountID: "test", ThreadID: "room@chatroom", SenderID: "sender", ServerID: "server-" + id,
		Type: 1, Direction: "incoming", Body: id, CreatedAt: time.Date(2026, 11, 26, 12, minute, 0, 0, time.UTC).Format(time.RFC3339Nano)}
}

func windowCatalogRow(m Message) map[string]any {
	return map[string]any{"id": m.ID, "threadId": m.ThreadID, "senderId": m.SenderID, "serverId": m.ServerID,
		"type": m.Type, "direction": m.Direction, "body": m.Body, "createdAt": m.CreatedAt}
}

func messageIDs(messages []Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func TestMessageWindowFindsOldQuoteAcrossPagesAndPreservesMediaMetadata(t *testing.T) {
	messages := map[string]Message{}
	for i := 0; i <= 5; i++ {
		id := fmt.Sprintf("m%d", i)
		messages[id] = windowMessage(id, i)
	}
	anchor := messages["m1"]
	anchor.Attachments = []Attachment{{Kind: "image", Filename: "reference.png", State: "available", MessageID: anchor.ID}}
	messages[anchor.ID] = anchor
	pages := map[string]struct {
		ids  []string
		next string
	}{"": {[]string{"m5", "m4"}, "p2"}, "p2": {[]string{"m3", "m2"}, "p3"}, "p3": {[]string{"m2", "m1", "m0"}, ""}}
	var catalogCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("window made a non-read or unauthenticated request")
		}
		switch {
		case r.URL.Path == "/v1/accounts/test/threads/room@chatroom/messages":
			if r.URL.Query().Get("limit") != "50" || r.URL.Query().Get("search") != "" {
				t.Error("catalog query changed", r.URL)
			}
			catalogCalls.Add(1)
			page, ok := pages[r.URL.Query().Get("cursor")]
			if !ok {
				t.Error("unexpected cursor", r.URL)
			}
			rows := make([]map[string]any, 0, len(page.ids))
			for _, id := range page.ids {
				rows = append(rows, windowCatalogRow(messages[id]))
			}
			var next *string
			if page.next != "" {
				next = &page.next
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": rows, "nextCursor": next})
		case strings.HasPrefix(r.URL.Path, "/v1/accounts/test/messages/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/accounts/test/messages/")
			message, ok := messages[id]
			if !ok {
				t.Error("unexpected message", id)
			}
			_ = json.NewEncoder(w).Encode(message)
		default:
			t.Error("unexpected route", r.URL)
		}
	}))
	defer server.Close()
	window, err := testSender(t, server.URL).MessageWindow(t.Context(), "room@chatroom", "m1", 1, 2)
	if err != nil || window.Coverage != "complete" || window.Anchor.ID != "m1" ||
		!reflect.DeepEqual(messageIDs(window.Before), []string{"m0"}) ||
		!reflect.DeepEqual(messageIDs(window.After), []string{"m2", "m3"}) ||
		len(window.Anchor.Attachments) != 1 || window.Anchor.Attachments[0].Filename != "reference.png" || catalogCalls.Load() != 3 {
		t.Fatalf("window=%+v catalog calls=%d err=%v", window, catalogCalls.Load(), err)
	}
}

func TestMessageWindowAbsentAnchorReturnsStrictTimePartial(t *testing.T) {
	anchor := windowMessage("old-quote", 2)
	rows := []Message{windowMessage("new", 4), windowMessage("same-time", 2), windowMessage("older", 1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("unexpected mutation")
		}
		if strings.Contains(r.URL.Path, "/threads/") {
			items := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				items = append(items, windowCatalogRow(row))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "nextCursor": nil})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/accounts/test/messages/")
		for _, row := range append(rows, anchor) {
			if row.ID == id {
				_ = json.NewEncoder(w).Encode(row)
				return
			}
		}
		t.Error("unexpected message", id)
	}))
	defer server.Close()
	window, err := testSender(t, server.URL).MessageWindow(t.Context(), "room@chatroom", anchor.ID, 1, 1)
	if err != nil || window.Coverage != "partial_anchor_not_cataloged" ||
		!reflect.DeepEqual(messageIDs(window.Before), []string{"older"}) ||
		!reflect.DeepEqual(messageIDs(window.After), []string{"new"}) {
		t.Fatalf("window=%+v err=%v", window, err)
	}
}

func TestMessageWindowRejectsCrossScopeAndMalformedTime(t *testing.T) {
	for _, tc := range []struct {
		name        string
		anchor      Message
		catalogRows []map[string]any
	}{
		{"cross thread anchor", func() Message { m := windowMessage("anchor", 1); m.ThreadID = "other"; return m }(), nil},
		{"wrong catalog thread", windowMessage("anchor", 1), []map[string]any{windowCatalogRow(windowMessage("future", 2)), func() map[string]any {
			r := windowCatalogRow(windowMessage("older", 0))
			r["threadId"] = "other"
			return r
		}()}},
		{"bad time", windowMessage("anchor", 1), []map[string]any{func() map[string]any {
			r := windowCatalogRow(windowMessage("future", 2))
			r["createdAt"] = "yesterday"
			return r
		}()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/threads/") {
					_ = json.NewEncoder(w).Encode(map[string]any{"items": tc.catalogRows, "nextCursor": nil})
					return
				}
				_ = json.NewEncoder(w).Encode(tc.anchor)
			}))
			defer server.Close()
			_, err := testSender(t, server.URL).MessageWindow(t.Context(), "room@chatroom", "anchor", 1, 1)
			if err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}

func TestMessageWindowNativeOrderBreakIsSkippedAndMarkedPartial(t *testing.T) {
	messages := map[string]Message{}
	for i := 0; i <= 5; i++ {
		id := fmt.Sprintf("m%d", i)
		messages[id] = windowMessage(id, i)
	}
	// m4 appears after m2 despite being newer. It cannot become a prior
	// message, while the remaining rows still establish a partial window.
	order := []string{"m5", "m2", "m4", "m1", "m0"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/threads/") {
			rows := make([]map[string]any, 0, len(order))
			for _, id := range order {
				rows = append(rows, windowCatalogRow(messages[id]))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": rows, "nextCursor": nil})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/accounts/test/messages/")
		_ = json.NewEncoder(w).Encode(messages[id])
	}))
	defer server.Close()
	window, err := testSender(t, server.URL).MessageWindow(t.Context(), "room@chatroom", "m1", 1, 2)
	if err != nil || window.Coverage != "partial_native_order" ||
		!reflect.DeepEqual(messageIDs(window.Before), []string{"m0"}) ||
		!reflect.DeepEqual(messageIDs(window.After), []string{"m2", "m5"}) {
		t.Fatalf("window=%+v err=%v", window, err)
	}
}

func TestMessageWindowEqualTimesUseNativePosition(t *testing.T) {
	ids := []string{"newest", "newer", "anchor", "older", "oldest"}
	messages := map[string]Message{}
	rows := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		m := windowMessage(id, 1)
		messages[id] = m
		rows = append(rows, windowCatalogRow(m))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/threads/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": rows, "nextCursor": nil})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/accounts/test/messages/")
		_ = json.NewEncoder(w).Encode(messages[id])
	}))
	defer server.Close()
	window, err := testSender(t, server.URL).MessageWindow(t.Context(), "room@chatroom", "anchor", 2, 2)
	if err != nil || window.Coverage != "complete" ||
		!reflect.DeepEqual(messageIDs(window.Before), []string{"oldest", "older"}) ||
		!reflect.DeepEqual(messageIDs(window.After), []string{"newer", "newest"}) {
		t.Fatalf("window=%+v err=%v", window, err)
	}
}

func TestMessageWindowSkipsChangedExactMessageTime(t *testing.T) {
	anchor := windowMessage("anchor", 1)
	newer := windowMessage("newer", 2)
	older := windowMessage("older", 0)
	rows := []map[string]any{windowCatalogRow(newer), windowCatalogRow(anchor), windowCatalogRow(older)}
	newer.CreatedAt = older.CreatedAt // Exact read no longer supports catalog placement.
	messages := map[string]Message{anchor.ID: anchor, newer.ID: newer, older.ID: older}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/threads/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": rows, "nextCursor": nil})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/accounts/test/messages/")
		_ = json.NewEncoder(w).Encode(messages[id])
	}))
	defer server.Close()
	window, err := testSender(t, server.URL).MessageWindow(t.Context(), "room@chatroom", anchor.ID, 1, 1)
	if err != nil || window.Coverage != "partial_native_order" || len(window.After) != 0 ||
		!reflect.DeepEqual(messageIDs(window.Before), []string{"older"}) {
		t.Fatalf("window=%+v err=%v", window, err)
	}
}

func TestMessageWindowCancellationReportsPartialCoverage(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	anchor := windowMessage("anchor", 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/threads/") {
			cancel()
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(anchor)
	}))
	defer server.Close()
	window, err := testSender(t, server.URL).MessageWindow(ctx, "room@chatroom", anchor.ID, 1, 1)
	if !errors.Is(err, context.Canceled) || window.Coverage != "partial_cancelled" || window.Anchor.ID != anchor.ID {
		t.Fatalf("window=%+v err=%v", window, err)
	}
}

func TestMessageWindowLargeRequestedRangeIsBatched(t *testing.T) {
	messages := make(map[string]Message)
	pages := map[string][]map[string]any{}
	for i := 120; i >= 0; i-- {
		id := fmt.Sprintf("m%03d", i)
		m := windowMessage(id, 0)
		m.CreatedAt = time.Date(2026, 11, 26, 12, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		messages[id] = m
		page := ""
		if i <= 70 && i > 20 {
			page = "p2"
		} else if i <= 20 {
			page = "p3"
		}
		pages[page] = append(pages[page], windowCatalogRow(m))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/threads/") {
			cursor := r.URL.Query().Get("cursor")
			if r.URL.Query().Get("limit") != "50" {
				t.Error("page batch changed")
			}
			var next *string
			switch cursor {
			case "":
				n := "p2"
				next = &n
			case "p2":
				n := "p3"
				next = &n
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": pages[cursor], "nextCursor": next})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/accounts/test/messages/")
		_ = json.NewEncoder(w).Encode(messages[id])
	}))
	defer server.Close()
	window, err := testSender(t, server.URL).MessageWindow(t.Context(), "room@chatroom", "m060", 60, 60)
	if err != nil || window.Coverage != "complete" || len(window.Before) != 60 || len(window.After) != 60 ||
		window.Before[0].ID != "m000" || window.After[59].ID != "m120" {
		t.Fatalf("window before=%d after=%d coverage=%s err=%v", len(window.Before), len(window.After), window.Coverage, err)
	}
}
