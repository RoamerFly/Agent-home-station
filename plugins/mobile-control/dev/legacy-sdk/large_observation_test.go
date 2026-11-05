package mobile

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"testing"
)

type fixedObservationBridge string

func (b fixedObservationBridge) Call(context.Context, string, json.RawMessage) (string, error) {
	return string(b), nil
}
func (b fixedObservationBridge) Close() error { return nil }

func TestHostPrivateScreenshotIsNotLimitedByOldMCPFrameBudget(t *testing.T) {
	c, _ := mobileFixture(t, nil)
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 512, 512))); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() <= 650<<10 {
		t.Fatal("fixture does not exercise the obsolete frame boundary")
	}
	snapshot := `{"id":"obs-0123456789abcdef0123456789abcdef","device_id":"lab","package":"test.app","width":512,"height":512,"nodes":[]}`
	raw, _ := json.Marshal(map[string]any{"text": snapshot, "media": []map[string]string{{"mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(encoded.Bytes())}}})
	observation, err := c.observe(t.Context(), fixedObservationBridge(raw), "lab")
	if err != nil || !bytes.Equal(observation.PNG, encoded.Bytes()) || observation.Width != 512 || observation.Height != 512 {
		t.Fatal("host-private screenshot was rejected or truncated", err)
	}
}
