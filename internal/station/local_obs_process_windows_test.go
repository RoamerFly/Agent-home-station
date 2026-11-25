//go:build windows

package station

import (
	"syscall"
	"testing"
)

func TestOBSListenerOwnsWildcardWithoutRelaxingComfy(t *testing.T) {
	const port, pid = 41761, 1978
	// A synthetic owner table tests both policies without listening on WLAN
	// or prompting Windows Firewall for each temporary Go test executable.
	rows := []localComfyTCPRowOwnerPID{{
		State: localComfyTCPStateListening, LocalAddr: 0,
		LocalPort: uint32(syscall.Ntohs(port)), OwnerPID: pid,
	}}
	if err := verifyLocalIPv4ListenerRows(rows, port, pid, true); err != nil {
		t.Fatal(err)
	}
	if err := verifyLocalIPv4ListenerRows(rows, port, pid+1, true); err == nil {
		t.Fatal("wrong owner accepted")
	}
	if err := verifyLocalIPv4ListenerRows(rows, port, pid, false); err == nil {
		t.Fatal("Comfy listener boundary changed")
	}
}
