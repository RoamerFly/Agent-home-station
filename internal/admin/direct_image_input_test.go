package admin

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"testing"
	"wechat-bot/internal/deepseek"
)

func TestDirectImageToolChecksActualSourceAndMode(t *testing.T) {
	f, p, n, _ := artifactPipelineFixture(t)
	f.manager.config.Behavior.DirectImageInput = true
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	asset, err := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: b.Bytes(), MIME: "image/png", Filename: "synthetic.png"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, clear := deepseek.WithImageSession(t.Context())
	defer clear()
	arg, _ := json.Marshal(map[string]string{"asset_id": asset.ID})
	if _, err = p.callArtifact(ctx, "artifact_view_image", arg); err != nil {
		t.Fatal(err)
	}
	if _, err = p.callArtifact(ctx, "artifact_view_image", json.RawMessage(`{"path":"../secret.png"}`)); err == nil {
		t.Fatal("outside task path accepted")
	}
	f.manager.config.Behavior.DirectImageInput = false
	if _, err = p.callArtifact(ctx, "artifact_view_image", arg); err == nil {
		t.Fatal("disabled route still executed")
	}
	f.manager.config.Behavior.DirectImageInput = true
	p.trace.Thread = "another-room@chatroom"
	if _, err = p.viewArtifactImage(ctx, arg); err == nil {
		t.Fatal("cross-conversation asset accepted")
	}
	if n.submits.Load() != 0 {
		t.Fatal("read-only image route sent a message")
	}
}
