package station

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

func TestStationWechatDocumentScope(t *testing.T) {
	testStationWechatDocument(t, stationDocumentTools{
		available: func(context.Context, string) bool { return true },
		inspect: func(ctx context.Context, in stationDocumentInput) (string, error) {
			dir := t.TempDir()
			if _, err := in.materialize(ctx, dir); err != nil {
				return "", err
			}
			if in.extension != ".docx" || in.offset != 0 || in.limit != 2 {
				t.Fatalf("unexpected document input: %#v", in)
			}
			raw, _ := json.Marshal(map[string]any{"kind": "docx", "sha256": in.record.SHA256,
				"bytes": in.record.Bytes, "text": "合成", "text_offset": 0, "text_total_characters": 4})
			view, err := stationDocumentResult(in, raw)
			if err != nil {
				return "", err
			}
			data, err := json.Marshal(view)
			return string(data), err
		},
	}, false)
}

func TestLiveStationWechatDocumentLPAC(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("AHS_LOCAL_PROJECT_DOCUMENT_LPAC_TEST") != "1" {
		t.Skip("set AHS_LOCAL_PROJECT_DOCUMENT_LPAC_TEST=1 for Windows document acceptance")
	}
	f := newLocalHomeFixture(t, filepath.Join(t.TempDir(), "host"), filepath.Join(t.TempDir(), "workspace"))
	f.handler.chat.projectTools.options = localProjectToolWindowsOptions(t)
	f.login()
	if code, out := f.request("POST", "/api/local/home", map[string]string{"action": "initialize"}, true); code != http.StatusOK {
		t.Fatalf("initialize home: %d %#v", code, out)
	}
	if code, out := f.request("PUT", "/api/local/project-tools/document-workshop", map[string]any{"action": "install", "approve_artifacts": true}, true); code != http.StatusOK {
		t.Fatalf("install document tool: %d %#v", code, out)
	}
	revision := localProjectToolRevision(t, localProjectToolsView(t, f))
	if code, out := f.request("PUT", "/api/local/project-tools/document-workshop", map[string]any{"action": "enable", "revision": revision}, true); code != http.StatusOK {
		t.Fatalf("enable document tool: %d %#v", code, out)
	}
	home := f.handler.chat.projectTools.home
	testStationWechatDocument(t, stationDocumentTools{available: home.stationDocumentAvailable, inspect: home.inspectStationDocument}, true)
	entries, err := os.ReadDir(filepath.Join(home.dataDir, "project-tool-tasks"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("isolated input remained after parsing: %v, %v", entries, err)
	}
}

func testStationWechatDocument(t *testing.T, documents stationDocumentTools, withPDF bool) {
	t.Helper()
	const account, thread, requester = "one", "room@chatroom", "owner"
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	makeMessage := func(id, sender, filename string) linuxnative.Message {
		return linuxnative.Message{ID: id, AccountID: account, ThreadID: thread, SenderID: sender, ServerID: id,
			Direction: "incoming", Type: 49, CreatedAt: now.Format(time.RFC3339),
			Body: "<msg><appmsg><type>6</type><title>" + filename + "</title></appmsg></msg>"}
	}
	messages := map[string]linuxnative.Message{
		"doc": makeMessage("doc", requester, "fixture.docx"), "other": makeMessage("other", "another-member", "fixture.docx"),
		"pdf": makeMessage("pdf", "another-member", "fixture.pdf"), "unseen": makeMessage("unseen", requester, "fixture.docx"),
		"own-pdf": makeMessage("own-pdf", requester, "fixture.pdf"),
	}
	foreign := makeMessage("foreign", requester, "fixture.docx")
	foreign.ThreadID = "different@chatroom"
	messages[foreign.ID] = foreign
	future := makeMessage("future", requester, "fixture.docx")
	future.CreatedAt = now.Add(2 * time.Minute).Format(time.RFC3339)
	messages[future.ID] = future
	docx := syntheticStationDOCX(t)
	pdf := syntheticStationPDF()
	fixtures := map[string][]byte{"doc": docx, "unseen": docx, "pdf": pdf, "own-pdf": pdf}
	mimes := map[string]string{"doc": localDOCXMIME, "unseen": localDOCXMIME, "pdf": "application/pdf", "own-pdf": "application/pdf"}
	var transfers atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		if r.URL.Path == "/v1/status" {
			write(map[string]any{"pid": 1234, "dataDir": root, "serverId": "document-fixture", "ownershipGeneration": "fixture"})
			return
		}
		if r.URL.Path == "/v1/accounts/one/threads/"+thread+"/messages" {
			write(map[string]any{"items": []linuxnative.Message{messages["doc"], messages["other"], messages["unseen"]}})
			return
		}
		if suffix, ok := strings.CutPrefix(r.URL.Path, "/v1/accounts/one/messages/"); ok {
			id, endpoint, _ := strings.Cut(suffix, "/")
			if message, exists := messages[id]; exists {
				switch r.Method + " " + endpoint {
				case "GET ":
					write(message)
					return
				case "GET media":
					write(map[string]any{"items": []map[string]any{{"accountId": account, "messageId": id,
						"kind": "file", "variant": "original", "state": "available"}}})
					return
				case "POST media-transfers":
					var args map[string]string
					if err := json.NewDecoder(r.Body).Decode(&args); err != nil || args["quality"] != "original" {
						t.Error("attachment transfer did not request the original", args, err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					transfers.Add(1)
					write(map[string]any{"id": "download-" + id, "accountId": account, "kind": "download", "state": "available", "assetId": "asset-" + id})
					return
				}
			}
		}
		if suffix, ok := strings.CutPrefix(r.URL.Path, "/v1/accounts/one/media/asset-"); ok {
			id, endpoint, _ := strings.Cut(suffix, "/")
			if data, exists := fixtures[id]; exists {
				if endpoint == "content" {
					_, _ = w.Write(data)
				} else {
					write(map[string]any{"id": "asset-" + id, "accountId": account, "messageId": id, "kind": "file",
						"state": "available", "variant": "original", "actualVariant": "original", "byteLength": len(data),
						"contentType": mimes[id], "sha256": fmt.Sprintf("%x", sha256.Sum256(data))})
				}
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: server.URL, Account: account, TokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := companion.NewStore(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	blocked := false
	tools := &stationAttachmentTools{native: native, store: memory, dir: root, documents: documents, blocked: func() bool { return blocked },
		grants: map[string]time.Time{thread: now.Add(-time.Minute)}}
	refs := map[string]*companion.SourceRef{}
	for id, message := range messages {
		if id == "unseen" || id == "foreign" || id == "future" {
			continue
		}
		ref := &companion.SourceRef{Account: account, Thread: thread, Sender: message.SenderID, MessageID: id, ServerID: id,
			MessageType: 49, OccurredAt: message.CreatedAt, BodyHash: fmt.Sprintf("%x", sha256.Sum256([]byte(message.Body))), Revision: 1,
			InstanceID: memory.SourceInstanceID()}
		cfg := companion.Defaults()
		cfg.Permissions.Remember = true
		if err := memory.Observe(companion.Input{Channel: "wechat", Account: account, Thread: thread, Sender: message.SenderID,
			TraceID: id, MessageTime: message.CreatedAt, Source: ref, Text: message.Body}, cfg, now); err != nil {
			t.Fatal(err)
		}
		refs[id] = ref
	}
	current := companion.Input{Channel: "wechat", Account: account, Thread: thread, Sender: requester, TraceID: "read-file",
		MessageTime: now.Format(time.RFC3339), UnreadMessages: []companion.ConversationEvent{
			{MessageID: "doc", Sender: requester, Source: refs["doc"]},
			{MessageID: "other", Sender: "another-member", Source: refs["other"]},
		}}
	tools.SetTrace(current)
	list, err := tools.List(t.Context())
	if err != nil || !slices.ContainsFunc(list, func(tool mcptools.Tool) bool { return tool.Name == "wechat_document_inspect" }) {
		t.Fatalf("document tool missing: %v %v", list, err)
	}
	call := func(args string) (string, error) {
		return tools.Call(t.Context(), "wechat_document_inspect", json.RawMessage(args))
	}
	for range 2 {
		if out, err := call(`{"message_id":"doc","text_limit":2}`); err != nil || !strings.Contains(out, `"text":"合成"`) ||
			!strings.Contains(out, `"next_text_offset":2`) || strings.Contains(out, root) {
			t.Fatalf("same-sender document reading: %s, %v", out, err)
		}
	}
	if transfers.Load() != 1 {
		t.Fatalf("repeat read transferred the original %d times", transfers.Load())
	}
	if _, err := call(`{"message_id":"other"}`); !errors.Is(err, wechatapp.ErrAttachmentDenied) {
		t.Fatalf("unquoted other-sender attachment was not denied: %v", err)
	}
	if withPDF {
		current.QuotedSource = refs["pdf"]
		tools.SetTrace(current)
		if out, err := call(`{"message_id":"pdf","page":1}`); err != nil || !strings.Contains(out, `"text":"Synthetic PDF text"`) {
			t.Fatalf("quoted PDF parsing: %s, %v", out, err)
		}
	}
	// The user sends the instruction in another turn with no quote. The
	// history lookup can now locate the same sender's file by message ID.
	current.TraceID, current.MessageTime = "later-request", now.Add(time.Minute).Format(time.RFC3339)
	current.UnreadMessages, current.QuotedSource = nil, nil
	tools.SetTrace(current)
	deny := func(id string) {
		t.Helper()
		if _, err := call(fmt.Sprintf(`{"message_id":%q,"text_limit":2}`, id)); !errors.Is(err, wechatapp.ErrAttachmentDenied) {
			t.Fatalf("historical source %s should be denied: %v", id, err)
		}
	}
	deny("doc")
	tools.reads = map[string]time.Time{thread: now.Add(-time.Minute)}
	list, err = tools.List(t.Context())
	if err != nil || !slices.ContainsFunc(list, func(tool mcptools.Tool) bool { return tool.Name == "wechat_document_inspect" }) {
		t.Fatalf("historical document tool missing: %v %v", list, err)
	}
	catalog, err := tools.Call(t.Context(), "wechat_read", json.RawMessage(`{"kind":"messages","search":"fixture.docx"}`))
	if err != nil || !strings.Contains(catalog, `"message_id":"doc"`) || !strings.Contains(catalog, `"read_scope":"file_metadata_only"`) {
		t.Fatalf("could not find historical attachment: %s, %v", catalog, err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(docx))
	for _, id := range []string{"doc", "unseen"} {
		out, err := call(fmt.Sprintf(`{"message_id":%q,"text_limit":2,"expected_sha256":%q}`, id, digest))
		if err != nil || !strings.Contains(out, `"text":"合成"`) {
			t.Fatalf("historical document reading: %s, %v", out, err)
		}
	}
	if out, err := call(fmt.Sprintf(`{"message_id":"doc","text_limit":2,"expected_sha256":%q}`, strings.Repeat("0", 64))); err == nil || out != "" {
		t.Fatalf("changed document digest was accepted: %s %v", out, err)
	}
	if withPDF {
		out, err := call(fmt.Sprintf(`{"message_id":"doc","text_offset":2,"text_limit":2,"expected_sha256":%q}`, digest))
		if err != nil || !strings.Contains(out, `"text":"正文"`) {
			t.Fatalf("historical DOCX continuation: %s, %v", out, err)
		}
		out, err = call(`{"message_id":"own-pdf","text_limit":9}`)
		if err != nil || !strings.Contains(out, `"text":"Synthetic"`) || !strings.Contains(out, `"next_text_offset":9`) {
			t.Fatalf("historical PDF parsing: %s, %v", out, err)
		}
	}
	for _, id := range []string{"other", "foreign", "future"} {
		deny(id)
	}
	if err := memory.Clear(companion.ScopeID("wechat", account, thread)); err != nil {
		t.Fatal(err)
	}
	deny("doc")
	deny("unseen")
	blocked = true
	if _, err := call(`{"message_id":"doc"}`); !errors.Is(err, wechatapp.ErrAttachmentDenied) {
		t.Fatalf("revoked grant accepted: %v", err)
	}
}

func syntheticStationDOCX(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, value := range map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>合成正文</w:t></w:r></w:p></w:body></w:document>`,
	} {
		part, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func syntheticStationPDF() []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	const stream = "BT /F1 12 Tf 20 40 Td (Synthetic PDF text) Tj ET\n"
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 120 60] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream), "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	var offsets []int
	for i, object := range objects {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return buf.Bytes()
}
