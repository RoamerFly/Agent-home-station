package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/plugins"
)

type outputAuthorityNative struct {
	*fakeTools
	uploads     int
	afterUpload func(int)
}

func (n *outputAuthorityNative) Upload(_ context.Context, filename, mime string, reader io.Reader, size int64) (linuxnative.UploadSource, error) {
	data, err := io.ReadAll(reader)
	if err != nil || int64(len(data)) != size {
		return linuxnative.UploadSource{}, errors.New("invalid synthetic upload")
	}
	n.uploads++
	if n.afterUpload != nil {
		n.afterUpload(n.uploads)
	}
	// A successful native upload can race revocation/cancellation. The core
	// must still reject the returned source instead of publishing it locally.
	return linuxnative.UploadSource{ID: fmt.Sprintf("synthetic-authority-part-%d", n.uploads), Filename: filename, ContentType: mime, ByteLength: size}, nil
}

func outputAuthorityFixture(t *testing.T) (*webFixture, *permittedTools, *outputAuthorityNative, plugins.Output, *assetOriginal) {
	t.Helper()
	f := setup(t)
	native := &outputAuthorityNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = native
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendFile = true
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.mu.Unlock()
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "synthetic-requester", TraceID: "synthetic-output-authority", Text: "prepare synthetic attachments"})
	output := plugins.Output{Text: "synthetic attachments, not sent", Media: []plugins.MediaResult{
		{Kind: "file", Data: []byte("synthetic first segment"), MIMEType: "text/plain", Filename: "synthetic-part-1.txt"},
		{Kind: "file", Data: []byte("synthetic second segment"), MIMEType: "text/plain", Filename: "synthetic-part-2.txt"},
	}}
	original := &assetOriginal{Data: []byte("synthetic original attachment"), MIME: "text/plain", Filename: "synthetic-original.txt"}
	return f, p, native, output, original
}

func assertOutputAuthorityDidNotPublish(t *testing.T, f *webFixture, p *permittedTools, native *outputAuthorityNative) {
	t.Helper()
	assets, err := f.manager.loadAssets()
	if err != nil || len(assets) != 0 {
		t.Fatal("denied output registered an asset", assets, err)
	}
	p.mediaMu.Lock()
	mediaCount := len(p.media)
	p.mediaMu.Unlock()
	if mediaCount != 0 {
		t.Fatal("denied output exposed generated media", mediaCount)
	}
	f.manager.tools.mu.Lock()
	uploadCount, sendCount := len(f.manager.tools.data.Uploads), len(f.manager.tools.data.Sends)
	f.manager.tools.mu.Unlock()
	if uploadCount != 0 || sendCount != 0 {
		t.Fatal("denied output registered native sources or send requests", uploadCount, sendCount)
	}
	if native.submits.Load() != 0 {
		t.Fatal("media preparation submitted a WeChat message")
	}
}

func TestOutputAuthorityRevokedBeforeStagingDoesNotUpload(t *testing.T) {
	f, p, native, output, original := outputAuthorityFixture(t)
	revoked := errors.New("synthetic output authority revoked")
	ctx := withOutputAuthority(t.Context(), func(context.Context) error { return revoked })
	result, err := p.stageOutputWithAsset(ctx, output, original)
	if !errors.Is(err, revoked) || result != "" || native.uploads != 0 {
		t.Fatal("revoked output reached native upload", native.uploads, result, err)
	}
	assertOutputAuthorityDidNotPublish(t, f, p, native)
}

func TestOutputAuthorityRevokedDuringFirstUploadStopsBundle(t *testing.T) {
	f, p, native, output, original := outputAuthorityFixture(t)
	revoked := errors.New("synthetic output authority revoked during upload")
	valid := true
	ctx := withOutputAuthority(t.Context(), func(context.Context) error {
		if !valid {
			return revoked
		}
		return nil
	})
	native.afterUpload = func(part int) {
		if part == 1 {
			valid = false
		}
	}
	result, err := p.stageOutputWithAsset(ctx, output, original)
	if !errors.Is(err, revoked) || result != "" || native.uploads != 1 {
		t.Fatal("revoked multi-part output continued or exposed a result", native.uploads, result, err)
	}
	assertOutputAuthorityDidNotPublish(t, f, p, native)
}

func TestOutputAuthorityContextCancellationStopsStaging(t *testing.T) {
	for _, duringUpload := range []bool{false, true} {
		name := "before_upload"
		if duringUpload {
			name = "during_first_upload"
		}
		t.Run(name, func(t *testing.T) {
			f, p, native, output, original := outputAuthorityFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			// Cancellation applies even to existing core callers without an
			// extra plugin authority callback.
			wantUploads := 0
			if duringUpload {
				wantUploads = 1
				native.afterUpload = func(part int) {
					if part == 1 {
						cancel()
					}
				}
			} else {
				cancel()
			}
			result, err := p.stageOutputWithAsset(ctx, output, original)
			if !errors.Is(err, context.Canceled) || result != "" || native.uploads != wantUploads {
				t.Fatal("cancelled output continued staging", native.uploads, result, err)
			}
			assertOutputAuthorityDidNotPublish(t, f, p, native)
		})
	}
}

func TestOutputAuthorityAllowsExistingCoreStagingWithoutGuard(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		name := "without_guard"
		if guarded {
			name = "authorized_guard"
		}
		t.Run(name, func(t *testing.T) {
			f, p, native, output, original := outputAuthorityFixture(t)
			ctx := t.Context()
			if guarded {
				ctx = withOutputAuthority(ctx, func(context.Context) error { return nil })
			}
			result, err := p.stageOutputWithAsset(ctx, output, original)
			if err != nil || native.uploads != 2 {
				t.Fatal("authorized core staging failed", native.uploads, err)
			}
			var staged struct {
				Media []companion.GeneratedMedia `json:"media"`
			}
			if json.Unmarshal([]byte(result), &staged) != nil || len(staged.Media) != 2 {
				t.Fatal("authorized bundle missing", result)
			}
			assets, err := f.manager.loadAssets()
			if err != nil || len(assets) != 1 {
				t.Fatal("authorized original was not registered", assets, err)
			}
			for _, item := range staged.Media {
				current, ok := p.Media(item.ID)
				if !ok || current.AssetID != assets[0].ID {
					t.Fatal("authorized source not exposed in this run", item)
				}
			}
			f.manager.tools.mu.Lock()
			uploadCount, sendCount := len(f.manager.tools.data.Uploads), len(f.manager.tools.data.Sends)
			f.manager.tools.mu.Unlock()
			if uploadCount != 2 || sendCount != 0 || native.submits.Load() != 0 {
				t.Fatal("staging lost sources or sent a message", uploadCount, sendCount, native.submits.Load())
			}
		})
	}
}
