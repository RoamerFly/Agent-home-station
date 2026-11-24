package station

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestStageComfyInputFromWorkspaceImage(t *testing.T) {
	var buffer bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	data := buffer.Bytes()
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	root := filepath.Join(t.TempDir(), "private", "input")
	name, err := stageComfyInput(root, sha, data, "png")
	if err != nil || name != "wechat-"+sha+".png" {
		t.Fatalf("stage: %q %v", name, err)
	}
	stored, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("stored input mismatch: %v", err)
	}
	if _, err := stageComfyInput(root, sha, data, "png"); err != nil {
		t.Fatalf("repeat upload: %v", err)
	}
	if _, err := stageComfyInput(root, sha, []byte("other"), "png"); err == nil {
		t.Fatal("mismatched content was accepted")
	}
}
