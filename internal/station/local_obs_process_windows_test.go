//go:build windows

package station

import (
	"net"
	"os"
	"testing"
)

func TestOBSListenerOwnsWildcardWithoutRelaxingComfy(t *testing.T) {
	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if err := localOBSListenerOwner(port, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := localOBSListenerOwner(port, os.Getpid()+1); err == nil {
		t.Fatal("wrong owner accepted")
	}
	if err := localComfyListenerOwner(port, os.Getpid()); err == nil {
		t.Fatal("Comfy listener boundary changed")
	}
}
