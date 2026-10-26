package mcptools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Explicit diagnostic against the isolated owner-created lab only. No action,
// installation, account access or WeChat send is performed by this test.
func TestLiveMobileBridgeObservationOverMCP(t *testing.T) {
	python, script, config, storage := os.Getenv("WECHAT_MOBILE_BRIDGE_PYTHON"), os.Getenv("WECHAT_MOBILE_BRIDGE_SCRIPT"), os.Getenv("WECHAT_MOBILE_BRIDGE_CONFIG"), os.Getenv("WECHAT_MOBILE_BRIDGE_STORAGE")
	if python == "" || script == "" || config == "" || storage == "" {
		t.Skip("explicit isolated mobile observation only")
	}
	h, err := New([]ServerConfig{{ID: "mobile", Transport: "stdio", Command: python, Args: []string{script, "--config", config, "--storage", storage}, Enabled: true, Tools: []string{"devices", "observe"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctx = WithSessionScope(ctx, "isolated-mobile-diagnostic")
	listed, err := h.List(ctx)
	if err != nil {
		t.Fatal("mobile tools unavailable", err)
	}
	if !hasTool(listed, "mobile__observe") || hasTool(listed, "mobile__act") {
		t.Fatal("read-only diagnostic tool scope invalid")
	}
	devices, err := h.Call(ctx, "mobile__devices", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal("mobile list failed", err)
	}
	var view struct{ Devices []struct{ ID, State string } }
	if json.Unmarshal([]byte(devices), &view) != nil || len(view.Devices) != 1 || view.Devices[0].ID != "lab" || view.Devices[0].State != "device" {
		t.Fatal("lab device not ready")
	}
	result, err := h.Call(ctx, "mobile__observe", json.RawMessage(`{"device_id":"lab"}`))
	if err != nil {
		t.Fatal("real MCP observation failed", err)
	}
	var response struct {
		Text  string `json:"text"`
		Media []struct {
			MIME string `json:"mime_type"`
			Data string `json:"data_base64"`
		} `json:"media"`
	}
	if json.Unmarshal([]byte(result), &response) != nil || len(response.Media) != 1 || response.Media[0].MIME != "image/png" {
		t.Fatal("MCP screenshot missing")
	}
	data, err := base64.StdEncoding.DecodeString(response.Media[0].Data)
	if err != nil || len(data) < 24 {
		t.Fatal("invalid real image")
	}
	var observation struct {
		Package       string `json:"package"`
		Width, Height int
		Nodes         []json.RawMessage `json:"nodes"`
	}
	if json.Unmarshal([]byte(response.Text), &observation) != nil || observation.Width != 480 || observation.Height != 800 || len(observation.Nodes) == 0 {
		t.Fatal("real UI structure missing")
	}
	t.Logf("Actual registered lab observation: %dx%d, %d nodes; no action or WeChat send", observation.Width, observation.Height, len(observation.Nodes))
}
