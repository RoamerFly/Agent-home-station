package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestInheritedMCPPipeProcess(t *testing.T) {
	if os.Getenv("WECHAT_MCP_PIPE_CHILD") == "1" {
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}
	if os.Getenv("WECHAT_MCP_PIPE_PARENT") != "1" {
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestInheritedMCPPipeProcess$")
	child.Env = append(os.Environ(), "WECHAT_MCP_PIPE_CHILD=1")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if child.Start() != nil {
		os.Exit(2)
	}
	_ = os.WriteFile(os.Getenv("WECHAT_MCP_PIPE_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600)
	time.Sleep(5 * time.Second)
	os.Exit(0)
}

func TestStdioCancellationClosesInheritedOutputPipe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "own-child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cancelAt := make(chan time.Time, 1)
	go func() {
		for ctx.Err() == nil {
			if _, err := os.Stat(path); err == nil {
				cancelAt <- time.Now()
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	var result json.RawMessage
	err := stdioRequest(ctx, ServerConfig{Command: os.Args[0], Args: []string{"-test.run=^TestInheritedMCPPipeProcess$"}, Env: map[string]string{"WECHAT_MCP_PIPE_PARENT": "1", "WECHAT_MCP_PIPE_PID": path}}, "tools/list", map[string]any{}, &result)
	data, readErr := os.ReadFile(path)
	if readErr == nil {
		pid, _ := strconv.Atoi(string(data))
		if child, e := os.FindProcess(pid); e == nil {
			_ = child.Kill()
			_ = child.Release()
		}
	}
	select {
	case when := <-cancelAt:
		if time.Since(when) > time.Second {
			t.Fatal("inherited stdout kept RPC blocked after cancellation")
		}
	default:
		t.Fatal("helper did not start; cancellation was not exercised")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not classified", err)
	}
}
