package agenthost

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// Explicit local opt-in: inputs are prepared private voice assets, the command
// creates a local WAV only. No native WeChat transport is involved.
func TestLiveSandboxGPTSoVITS(t *testing.T) {
	file := os.Getenv("WECHAT_GSV_LIVE_INPUT")
	if file == "" {
		t.Skip("explicit local voice inference input required")
	}
	requireLinuxSandbox(t)
	var in struct {
		Public, Private string
		Input           Input
	}
	b, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(b, &in) != nil || in.Public == "" || in.Private == "" {
		t.Fatal("invalid explicit inference input", err)
	}
	cfg := Defaults()
	cfg.Mode = "full"
	cfg.TimeoutSeconds = 300
	cfg.Roots = []string{in.Private}
	cfg.Isolation = &Isolation{GPU: true, ReadOnly: []string{in.Public}}
	args, _ := json.Marshal(in.Input)
	result, err := (Host{cfg}).Call(context.Background(), "server_exec", args)
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["exit_code"] != 0 {
		t.Fatal(out)
	}
	t.Log(out["output"])
}
