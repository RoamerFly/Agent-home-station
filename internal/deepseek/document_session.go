package deepseek

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// AttachDocument uploads original bytes, never locally extracted text. Files
// are deduplicated within this run, expire in one hour, and are removed on exit.
// Only the next successful model request includes them to avoid repeated billing.
func (c *Client) AttachDocument(ctx context.Context, name string, data []byte, rich bool) (bool, error) {
	s, ok := ctx.Value(imageSessionKey{}).(*imageSession)
	if !ok {
		return false, errors.New("document_session_unavailable")
	}
	if !c.SupportsDirectImages() {
		return false, errors.New("document_input_requires_deepseek_flash")
	}
	purpose := "text_recognition"
	if rich {
		purpose = "rich_layout_recognition"
	}
	max := 100 << 20
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".csv", ".json", ".xlsx", ".xls", ".epub", ".mobi":
		purpose = "user_data"
		max = 32 << 20
	case ".pdf", ".doc", ".docx", ".ppt", ".pptx":
	default:
		return false, errors.New("native_document_format_unsupported")
	}
	if len(data) == 0 || len(data) > max {
		return false, fmt.Errorf("document_provider_size_limit_%d_bytes", max)
	}
	key := fmt.Sprintf("%x:%s", sha256.Sum256(data), purpose)
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := s.files[key]; id != "" {
		s.pendingFiles = appendUnique(s.pendingFiles, id)
		return true, nil
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("purpose", purpose)
	w.WriteField("expires_after[anchor]", "created_at")
	w.WriteField("expires_after[seconds]", "3600")
	f, err := w.CreateFormFile("file", filepath.Base(name))
	if err != nil {
		return false, err
	}
	if _, err = f.Write(data); err != nil {
		return false, err
	}
	if err = w.Close(); err != nil {
		return false, err
	}
	endpoint := strings.TrimSuffix(c.endpoint, "/chat/completions") + "/files"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, &body)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", w.FormDataContentType())
	client := *c.http
	client.Timeout = 4 * time.Minute
	response, err := client.Do(req)
	if err != nil {
		return false, errors.New("document_upload_transport_failed")
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return false, fmt.Errorf("document_upload_http_%d", response.StatusCode)
	}
	var result struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result) != nil || !strings.HasPrefix(result.ID, "file-") {
		return false, errors.New("document_upload_invalid_response")
	}
	if s.files == nil {
		s.files = map[string]string{}
	}
	s.files[key] = result.ID
	s.pendingFiles = appendUnique(s.pendingFiles, result.ID)
	s.cleanup = append(s.cleanup, func() {
		clean, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, e := http.NewRequestWithContext(clean, "DELETE", endpoint+"/"+url.PathEscape(result.ID), nil)
		if e != nil {
			return
		}
		r.Header.Set("Authorization", "Bearer "+c.key)
		resp, e := client.Do(r)
		if e == nil {
			resp.Body.Close()
		}
	})
	return false, nil
}

func appendUnique(ids []string, id string) []string {
	for _, v := range ids {
		if v == id {
			return ids
		}
	}
	return append(ids, id)
}
