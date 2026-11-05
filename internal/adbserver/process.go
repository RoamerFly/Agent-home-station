package adbserver

import (
	"context"
	"os"
	"os/exec"
)

var command = exec.Command

// defaultStarter starts a foreground server. platformStart owns the process
// tree boundary; it never starts the detached `start-server` form.
func defaultStarter(_ context.Context, adbPath string, endpoint Endpoint) (Process, error) {
	// Acquire's context belongs to one request, while this foreground server is
	// shared by leases. Only Manager release controls the server lifetime.
	cmd := command(adbPath, serverArgs(endpoint)...)
	cmd.Env = controlledServerEnv()
	return platformStart(cmd)
}

func serverArgs(endpoint Endpoint) []string {
	// `server nodaemon` keeps the actual server as our direct, foreground
	// child. Do not replace this with `start-server`: that form detaches and
	// makes ownership/PID cleanup ambiguous.
	// Server listen uses `localhost`, which AOSP accepts as a local listen
	// socket. Clients deliberately use numeric 127.0.0.1 (a non-local AOSP
	// socket spec) to prevent automatic daemon launch on connection failure.
	return []string{"-L", "tcp:localhost:5037", "server", "nodaemon"}
}

func controlledServerEnv() []string {
	// The native server needs a minimal OS/user profile to locate its approved
	// ADB key material. It must not inherit provider keys, PATH, or alternate
	// ADB endpoint selectors from the desktop host.
	allowed := []string{"HOME", "USER", "LANG", "TZ", "TMPDIR", "TEMP", "TMP", "SystemRoot", "WINDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA"}
	env := make([]string, 0, len(allowed))
	for _, key := range allowed {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
