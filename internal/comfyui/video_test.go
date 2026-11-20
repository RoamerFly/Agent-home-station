package comfyui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestMP4ScopesApprovedOutputAndRejectsOtherJobs(t *testing.T) {
	ref, err := NewJobRef(digest([]byte(t.Name())), "9")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"9":{"class_type":"SaveVideo","inputs":{"video":["8",0],"filename_prefix":"owner-template","format":"webm"}}}`)
	prepared, err := PrepareMP4(raw, digest(raw), ref)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Prompt map[string]struct{ Inputs map[string]any }
	}
	if err := json.Unmarshal(prepared.body, &body); err != nil {
		t.Fatal(err)
	}
	inputs := body.Prompt["9"].Inputs
	if inputs["filename_prefix"] != ref.subfolder()+"/video" || inputs["format"] != "mp4" || inputs["codec"] != "h264" || inputs["format.codec"] != "h264" {
		t.Fatal("wrong approved output scope", inputs)
	}
	for _, scope := range []string{ref.subfolder(), "unrelated-job"} {
		t.Run(scope, func(t *testing.T) {
			fixture := []byte("\x00\x00\x00\x0cftypisom")
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/jobs/" + ref.ID:
					fmt.Fprintf(w, `{"id":%q,"status":"completed","execution_status":{"status_str":"success","completed":true},"outputs":{"9":{"images":[{"filename":"video_00001_.mp4","subfolder":%q,"type":"output"}],"animated":[true]}}}`, ref.ID, scope)
				case "/view":
					if scope != ref.subfolder() || r.URL.Query().Get("subfolder") != ref.subfolder() || r.URL.Query().Get("filename") != "video_00001_.mp4" {
						t.Error("unexpected MP4 readback", r.URL)
					}
					_, _ = w.Write(fixture)
				default:
					t.Errorf("unexpected route %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			video, err := client.FetchMP4(t.Context(), ref)
			if scope != ref.subfolder() {
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("mismatched output accepted: %v", err)
				}
				return
			}
			if err != nil || video.SHA256 != digest(fixture) {
				t.Fatalf("approved MP4 unavailable: %v", err)
			}
		})
	}
}
