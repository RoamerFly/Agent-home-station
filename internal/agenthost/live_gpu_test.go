package agenthost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveSandboxCUDAUsesGPUWithoutHostAccess(t *testing.T) {
	public := os.Getenv("WECHAT_GPU_PUBLIC_ROOT")
	if public == "" {
		t.Skip("explicit managed CUDA runtime required")
	}
	requireLinuxSandbox(t)
	scratch := t.TempDir()
	cfg := Defaults()
	cfg.Mode = "full"
	cfg.TimeoutSeconds = 180
	cfg.Roots = []string{scratch}
	cfg.Isolation = &Isolation{GPU: true, ReadOnly: []string{public}}
	python := filepath.Join(public, "runtimes/linux-music-env/bin/python3.10")
	cmd := "'" + python + "' -c 'import torch; assert torch.cuda.is_available(); x=torch.ones(8,device=\"cuda\"); print(\"verified-cuda\",torch.cuda.get_device_name(0),int(x.sum().item()))' && test ! -e /home/fly && test ! -e /run/user && test ! -e /mnt/c"
	args, _ := json.Marshal(Input{Path: scratch, Command: cmd})
	v, e := (Host{cfg}).Call(context.Background(), "server_exec", args)
	if e != nil {
		t.Fatal(e)
	}
	result := v.(map[string]any)
	if result["exit_code"] != 0 || !strings.Contains(result["output"].(string), "verified-cuda") {
		t.Fatal(result)
	}
	t.Log(result["output"])
}
