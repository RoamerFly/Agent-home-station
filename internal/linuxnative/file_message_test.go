package linuxnative

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFileMessageMetadataDoesNotExposeCDNSecrets(t *testing.T) {
	body := `<msg><appmsg><type>6</type><title>资料&amp;笔记.txt</title><appattach><aeskey>SECRET</aeskey></appattach></appmsg></msg>`
	if got := (Message{Type: 49, Body: body}).FileAttachmentName(); got != "资料&笔记.txt" {
		t.Fatal(got)
	}
	for _, m := range []Message{{Type: 1, Body: body}, {Type: 49, Body: `<appmsg><type>5</type><title>Link</title></appmsg>`}, {Type: 49, Body: `<msg><appmsg>`}} {
		if m.FileAttachmentName() != "" {
			t.Fatal("not a file")
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"id": "file-one", "type": 49, "body": body}}, "nextCursor": nil})
	}))
	defer srv.Close()
	page, err := testSender(t, srv.URL).Catalog(context.Background(), "messages", "room@chatroom", "", "")
	if err != nil || page.Items[0]["file_name"] != "资料&笔记.txt" {
		t.Fatal(page, err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatal("file transport secret leaked")
	}
}
