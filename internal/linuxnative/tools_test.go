package linuxnative

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestToolsUploadSubmitAndReadOnlyLookup(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing bearer")
		}
		switch r.URL.Path {
		case "/v1/accounts/test/uploads":
			if err := r.ParseMultipartForm(1024); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			b, _ := io.ReadAll(file)
			file.Close()
			if string(b) != "bytes" {
				t.Error("source changed")
			}
			w.WriteHeader(201)
			write(w, `{"id":"source-one"}`)
		case "/v1/accounts/test/sends":
			sends.Add(1)
			var input map[string]any
			json.NewDecoder(r.Body).Decode(&input)
			if input["sourceId"] != "source-one" || input["kind"] != "image" || r.Header.Get("Idempotency-Key") != "same-key" {
				t.Error("bad send contract")
			}
			w.WriteHeader(202)
			write(w, `{"id":"job-one","accountId":"test","threadId":"filehelper","kind":"send","state":"queued","phase":"upload"}`)
		case "/v1/accounts/test/sends/by-idempotency-key":
			if r.Method != "GET" || r.URL.Query().Get("key") != "same-key" {
				t.Error("lookup is not a keyed GET")
			}
			write(w, `{"id":"job-one","accountId":"test","threadId":"filehelper","state":"accepted","serverMessageId":"18446744073709551615"}`)
		default:
			t.Error("unexpected route", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s := testSender(t, server.URL)
	source, err := s.Upload(context.Background(), "image.png", "image/png", &textReader{value: []byte("bytes")}, 5)
	if err != nil || source.ID != "source-one" {
		t.Fatal(source, err)
	}
	status, err := s.Submit(context.Background(), ToolSend{ThreadID: "filehelper", Kind: "image", SourceID: source.ID, IdempotencyKey: "same-key"})
	if err != nil || status.ID != "job-one" || status.Kind != "image" {
		t.Fatal(status, err)
	}
	status, err = s.LookupSend(context.Background(), "same-key")
	if err != nil || status.ServerID != "18446744073709551615" || sends.Load() != 1 {
		t.Fatal(status, err)
	}
}

func TestQuoteAdmissionFailureHasSafeCodeWithoutLeakingNativeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		write(w, `{"error":{"code":"unsupported_quote_type","message":"private-account-data"}}`)
	}))
	defer server.Close()
	s := testSender(t, server.URL)
	_, err := s.Submit(context.Background(), ToolSend{Kind: "text", ThreadID: "filehelper", Text: "reply", IdempotencyKey: "quote-once"})
	if err == nil || QuoteAdmissionReason(err) != "unsupported_quote_type" || strings.Contains(err.Error(), "private-account-data") {
		t.Fatal(err)
	}
}

func TestResumeMissingMediaUsesFreshIdempotencyKeyAndNewJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/accounts/test/tools/media/old-job/resume-missing" || r.Header.Get("Idempotency-Key") != "fresh-resume-key" {
			t.Fatalf("bad resume route or idempotency key: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["idempotencyKey"] != "fresh-resume-key" {
			t.Fatal("missing resume key", body)
		}
		w.WriteHeader(http.StatusAccepted)
		write(w, `{"id":"new-job","accountId":"test","threadId":"filehelper","sourceId":"source-one","state":"queued","phase":"voice"}`)
	}))
	defer server.Close()
	s := testSender(t, server.URL)
	status, err := s.ResumeMissingMediaJob(context.Background(), "old-job", "fresh-resume-key")
	if err != nil || status.ID != "new-job" || status.ThreadID != "filehelper" || status.SourceID != "source-one" || status.State != "queued" {
		t.Fatal(status, err)
	}
}

func TestVoiceStatusPreservesPacingAndReceiptEvidenceWithoutPrivateDetails(t *testing.T) {
	s := &Sender{account: "test"}
	status, err := s.decodeToolStatus([]byte(`{"id":"job-voice","accountId":"test","state":"running","phase":"pacing","result":{"private":"must not expose","voiceFailureStage":"voice_upload_exchange_outcome_unknown","chunks":[{"index":0,"state":"accepted","durationMs":40124,"pacingMs":0,"submittedAtMs":123,"acceptedAtMs":456,"serverMessageId":"18446744073709551615"},{"index":1,"state":"prepared","durationMs":40124,"pacingMs":41999,"serverMessageId":null},{"index":9,"state":"accepted","durationMs":1,"pacingMs":0}]}}`))
	if err != nil || len(status.VoiceChunks) != 2 {
		t.Fatal(status, err)
	}
	if !status.VoiceChunks[0].HasReceipt || status.VoiceChunks[1].HasReceipt || status.VoiceChunks[1].PacingMS != 41999 {
		t.Fatal(status)
	}
	if status.VoiceChunks[0].AcceptedAtMS != 456 || status.VoiceFailureStage != "voice_upload_exchange_outcome_unknown" {
		t.Fatal(status)
	}
	encoded, _ := json.Marshal(status)
	if strings.Contains(string(encoded), "must not expose") {
		t.Fatal("private result copied")
	}
	status, err = s.decodeToolStatus([]byte(`{"id":"job-voice","result":{"voiceFailureStage":"private raw exception","voiceTransportKind":"voice_transport_timeout","voiceFailedOffset":0}}`))
	if err != nil || status.VoiceFailureStage != "" || status.VoiceTransportKind != "voice_transport_timeout" || status.VoiceFailedOffset == nil || *status.VoiceFailedOffset != 0 {
		t.Fatal(status, err)
	}
	status, err = s.decodeToolStatus([]byte(`{"id":"job-voice","result":{"voiceTransportKind":"private exception and token","voiceFailedOffset":186011}}`))
	if err != nil || status.VoiceTransportKind != "" || status.VoiceFailedOffset != nil {
		t.Fatal(status, err)
	}
}

func TestFullMusicStatusRetainsAllVoiceChunksBeyondFive(t *testing.T) {
	chunks := []map[string]any{}
	for i := 0; i < 6; i++ {
		duration := 55000
		if i == 5 {
			duration = 25000
		}
		chunks = append(chunks, map[string]any{"index": i, "state": "accepted", "durationMs": duration, "serverMessageId": strconv.Itoa(12345 + i)})
	}
	b, _ := json.Marshal(map[string]any{"id": "whole-song", "state": "accepted", "serverMessageId": "12350", "result": map[string]any{"chunks": chunks}})
	sender := &Sender{account: "test"}
	status, e := sender.decodeToolStatus(b)
	if e != nil || len(status.VoiceChunks) != 6 {
		t.Fatal("song tail receipts dropped", e, status)
	}
	total := int64(0)
	for _, chunk := range status.VoiceChunks {
		if !chunk.HasReceipt || chunk.DurationMS > 60000 {
			t.Fatal("invalid chunk", chunk)
		}
		total += chunk.DurationMS
	}
	if total != 300000 {
		t.Fatal("full audio not represented", total)
	}
}

type textReader struct{ value []byte }

type zeroUploadReader struct{}

func (zeroUploadReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestOriginalFileUploadStreamsBeyondLegacy64MiBLimit(t *testing.T) {
	const size int64 = 65 << 20
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		n, err := io.Copy(io.Discard, part)
		if err != nil || n != size {
			t.Error(n, err)
		}
		if _, err = form.NextPart(); err != io.EOF {
			t.Error(err)
		}
		w.WriteHeader(201)
		write(w, `{"id":"large-original-file"}`)
	}))
	defer server.Close()
	sender := testSender(t, server.URL)
	upload, err := sender.Upload(context.Background(), "original.flac", "audio/flac", io.LimitReader(zeroUploadReader{}, size), size)
	if err != nil || upload.ByteLength != size {
		t.Fatal(upload, err)
	}
	if _, err = sender.Upload(context.Background(), "too-large.bin", "application/octet-stream", zeroUploadReader{}, 512<<20+1); err == nil {
		t.Fatal("protocol size bound ignored")
	}
}

func (r *textReader) Read(b []byte) (int, error) {
	if len(r.value) == 0 {
		return 0, io.EOF
	}
	n := copy(b, r.value)
	r.value = r.value[n:]
	return n, nil
}

func TestPartialCancellationCannotBecomeCompleteReceipt(t *testing.T) {
	s := &Sender{account: "a"}
	status, err := s.decodeToolStatus([]byte(`{"id":"job","accountId":"a","state":"cancelled","cancelRequested":true,"result":{"chunks":[{"index":0,"state":"accepted","durationMs":1000,"serverMessageId":"22"},{"index":1,"state":"submitting","durationMs":1000},{"index":2,"state":"cancelled","durationMs":1000}]}}`))
	if err != nil || len(status.VoiceChunks) != 3 || status.VoiceChunks[2].State != "cancelled" || status.WithVoiceReceipts().State == "accepted" {
		t.Fatal(status, err)
	}
}
