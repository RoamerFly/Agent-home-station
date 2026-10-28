package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type syntheticUploadZeros struct{}

func (syntheticUploadZeros) Read(b []byte) (int, error) { clear(b); return len(b), nil }

type streamingFileNative struct {
	*fakeTools
	bytes      int64
	hash       string
	beforeRead func()
}

func (*streamingFileNative) Capabilities(context.Context) (linuxnative.Capabilities, error) {
	return linuxnative.Capabilities{MediaKinds: []string{"file"}, MediaSendMaxBytes: map[string]int64{"file": 512 << 20}}, nil
}
func (n *streamingFileNative) Upload(_ context.Context, name, typ string, src io.Reader, size int64) (linuxnative.UploadSource, error) {
	if n.beforeRead != nil {
		n.beforeRead()
	}
	h := sha256.New()
	var err error
	n.bytes, err = io.Copy(h, src)
	n.hash = hex.EncodeToString(h.Sum(nil))
	if err != nil || n.bytes != size {
		return linuxnative.UploadSource{}, errors.New("incomplete native upload")
	}
	return linuxnative.UploadSource{ID: "streamed-synthetic-file", Filename: name, ContentType: typ, ByteLength: size}, nil
}

func TestConsoleUploadStreamsBeyond64MiBWithoutSendingOrGlobalTemp(t *testing.T) {
	f := setup(t)
	n := &streamingFileNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = n
	f.login()
	const size int64 = 65<<20 + 17
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	done := make(chan error, 1)
	go func() {
		part, err := form.CreateFormFile("file", "合成原件.bin")
		if err == nil {
			_, err = io.CopyN(part, syntheticUploadZeros{}, size)
		}
		if err == nil {
			err = form.WriteField("kind", "file")
		}
		if err == nil {
			err = form.Close()
		}
		writer.CloseWithError(err)
		done <- err
	}()
	req, _ := http.NewRequest("POST", f.server.URL+"/api/tools/upload", reader)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Origin", f.server.URL)
	req.Header.Set("X-CSRF-Token", f.csrf)
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 201 || n.bytes != size || n.submits.Load() != 0 {
		t.Fatal(res.StatusCode, string(body), n.bytes)
	}
	files, err := os.ReadDir(filepath.Join(f.manager.dir, "manual-uploads"))
	if err != nil || len(files) != 0 {
		t.Fatal("private staging not removed", files, err)
	}
	hash := sha256.New()
	io.CopyN(hash, syntheticUploadZeros{}, size)
	if n.hash != hex.EncodeToString(hash.Sum(nil)) {
		t.Fatal("stream changed bytes")
	}
}

func smallUploadRequest(t *testing.T, duplicate, kindFirst bool) *http.Request {
	t.Helper()
	var b bytes.Buffer
	form := multipart.NewWriter(&b)
	if kindFirst {
		form.WriteField("kind", "file")
	}
	part, _ := form.CreateFormFile("file", "example.txt")
	part.Write([]byte("synthetic content"))
	if !kindFirst || duplicate {
		form.WriteField("kind", "file")
	}
	form.Close()
	r := httptest.NewRequest("POST", "/api/tools/upload", bytes.NewReader(b.Bytes()))
	r.Header.Set("Content-Type", form.FormDataContentType())
	return r
}

func TestPrivateUploadCleanupForCapacityCancellationMalformedAndDuplicates(t *testing.T) {
	for _, test := range []string{"capacity", "cancel", "truncated", "duplicate", "kind_first"} {
		t.Run(test, func(t *testing.T) {
			dir := t.TempDir()
			r := smallUploadRequest(t, test == "duplicate", test == "duplicate" || test == "kind_first")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			free := func(string) (uint64, error) { return 1 << 30, nil }
			if test == "capacity" {
				free = func(string) (uint64, error) { return 0, nil }
			}
			if test == "cancel" {
				cancel()
			}
			if test == "truncated" {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body[:len(body)-80]))
			}
			u, err := stageManualUpload(ctx, r, dir, 1024, free)
			if test == "kind_first" {
				if err != nil || u.kind != "file" {
					t.Fatal(u, err)
				}
				u.close()
			} else if err == nil {
				u.close()
				t.Fatal("invalid upload accepted")
			}
			if test == "capacity" {
				var failure *uploadFailure
				if !errors.As(err, &failure) || failure.status != 507 {
					t.Fatal(err)
				}
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatal("partial stage remained", files, err)
			}
		})
	}
}

func TestLargeOriginalReuseVerifiesAndStreamsInsteadOf64MiBRead(t *testing.T) {
	f := setup(t)
	n := &streamingFileNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = n
	f.manager.config.Companion.Permissions.SendFile = true
	file, err := os.CreateTemp(t.TempDir(), "original-*")
	if err != nil {
		t.Fatal(err)
	}
	const size int64 = 65 << 20
	if _, err = io.CopyN(file, syntheticUploadZeros{}, size); err != nil {
		t.Fatal(err)
	}
	file.Close()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "large-synthetic-file"}
	a, err := f.manager.rememberStreamAsset(in, file.Name(), "large-original.bin", "application/octet-stream", size)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	p.SetTrace(in)
	result, err := p.reuseAsset(context.Background(), a.ID, "file")
	if err != nil || n.bytes != size || n.hash != a.Hash || !strings.Contains(result, `"kind":"file"`) || n.submits.Load() != 0 {
		t.Fatal(result, err, n.bytes)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if f, err := f.manager.openVerifiedAsset(ctx, a); err == nil {
		f.Close()
		t.Fatal("verification ignored cancellation")
	}
}

func TestCachedFileChangedDuringUploadIsNotPublishedForSending(t *testing.T) {
	f := setup(t)
	n := &streamingFileNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = n
	f.manager.config.Companion.Permissions.SendFile = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "synthetic-changing-file"}
	a, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: []byte("original"), MIME: "text/plain", Filename: "original.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.beforeRead = func() {
		// Same inode and length: a pre-upload stat/hash alone would miss this.
		if err := os.WriteFile(f.manager.assetPath(a.Hash), []byte("modified"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	p.SetTrace(in)
	if _, err = p.reuseAsset(context.Background(), a.ID, "file"); err == nil || !strings.Contains(err.Error(), "changed_during_upload") {
		t.Fatal(err)
	}
	assets, err := f.manager.loadAssets()
	if err != nil || assets[0].Sources["file"] != "" || n.submits.Load() != 0 {
		t.Fatal("changed file became reusable send", assets, err)
	}
}

func TestUploadDeadlineCanExceedDefaultOrBeDisabled(t *testing.T) {
	for _, seconds := range []string{"1800", "0", "-1", "bad", "9223372036854775807"} {
		r := httptest.NewRequest("POST", "/api/tools/upload?timeout_seconds="+seconds, nil)
		ctx, cancel, err := manualUploadContext(httptest.NewRecorder(), r)
		if seconds == "1800" || seconds == "0" {
			if err != nil {
				t.Fatal(err)
			}
			deadline, present := ctx.Deadline()
			if seconds == "0" && present || seconds == "1800" && (!present || time.Until(deadline) < 29*time.Minute) {
				t.Fatal(seconds, deadline, present)
			}
			cancel()
		} else if err == nil {
			cancel()
			t.Fatal("invalid timeout accepted", seconds)
		}
	}
	if bytes, err := availableStorageBytes(t.TempDir()); err != nil || bytes == 0 {
		t.Fatal(bytes, err)
	}
}

func TestAuthenticatedUploadOverridesShortJSONBodyDeadline(t *testing.T) {
	f := setup(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.login()
	server := httptest.NewUnstartedServer(f.server.Config.Handler)
	server.Config.ReadTimeout = 50 * time.Millisecond
	server.Start()
	defer server.Close()
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	done := make(chan error, 1)
	go func() {
		part, err := form.CreateFormFile("file", "slow.txt")
		if err == nil {
			_, err = part.Write([]byte("start"))
		}
		time.Sleep(150 * time.Millisecond)
		if err == nil {
			_, err = part.Write([]byte("end"))
		}
		if err == nil {
			err = form.WriteField("kind", "file")
		}
		if err == nil {
			err = form.Close()
		}
		writer.CloseWithError(err)
		done <- err
	}()
	req, _ := http.NewRequest("POST", server.URL+"/api/tools/upload", reader)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Origin", server.URL)
	req.Header.Set("X-CSRF-Token", f.csrf)
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 201 {
		t.Fatal(res.StatusCode, string(body))
	}
}
