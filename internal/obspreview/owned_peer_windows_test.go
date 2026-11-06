//go:build windows

package obspreview

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestVerifyEncoderPeerAcceptsExactLoopbackConnection(t *testing.T) {
	client := loopbackConnection(t, "127.0.0.1:0")
	defer client.Close()
	if err := verifyEncoderPeer(context.Background(), client, uint32(os.Getpid())); err != nil {
		t.Fatalf("verified loopback peer: %v", err)
	}
}

func TestVerifyEncoderPeerRejectsWrongProcess(t *testing.T) {
	client := loopbackConnection(t, "127.0.0.1:0")
	defer client.Close()
	if err := verifyEncoderPeer(context.Background(), client, uint32(os.Getpid()+1)); !errors.Is(err, ErrStreamEndpoint) {
		t.Fatalf("wrong process = %v", err)
	}
}

func TestVerifyEncoderPeerRejectsWildcardListener(t *testing.T) {
	client := loopbackConnection(t, "0.0.0.0:0")
	defer client.Close()
	if err := verifyEncoderPeer(context.Background(), client, uint32(os.Getpid())); !errors.Is(err, ErrStreamEndpoint) {
		t.Fatalf("wildcard listener = %v", err)
	}
}

func TestVerifyEncoderPeerRejectsIPv6ListenerOnControlPort(t *testing.T) {
	listener4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener4.Close()
	port := listener4.Addr().(*net.TCPAddr).Port
	listener6, err := net.Listen("tcp6", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("IPv6 loopback listener on IPv4 control port: %v", err)
	}
	defer listener6.Close()
	client := acceptedLoopbackConnection(t, listener4.(*net.TCPListener))
	defer client.Close()
	if err := verifyEncoderPeer(context.Background(), client, uint32(os.Getpid())); !errors.Is(err, ErrStreamEndpoint) {
		t.Fatalf("IPv6 listener = %v", err)
	}
}

func TestVerifyEncoderPeerRejectsCanceledContext(t *testing.T) {
	client := loopbackConnection(t, "127.0.0.1:0")
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := verifyEncoderPeer(ctx, client, uint32(os.Getpid())); !errors.Is(err, ErrStreamEndpoint) {
		t.Fatalf("canceled context = %v", err)
	}
}

func loopbackConnection(t *testing.T, address string) *net.TCPConn {
	t.Helper()
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		t.Fatal("TCP listen did not return TCP listener")
	}
	return acceptedLoopbackConnection(t, tcpListener)
}

func acceptedLoopbackConnection(t *testing.T, tcpListener *net.TCPListener) *net.TCPConn {
	t.Helper()
	accepted := make(chan *net.TCPConn, 1)
	failed := make(chan error, 1)
	go func() {
		connection, err := tcpListener.AcceptTCP()
		if err != nil {
			failed <- err
			return
		}
		accepted <- connection
	}()
	client, err := net.DialTimeout("tcp4", tcpListener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tcp, ok := client.(*net.TCPConn)
	if !ok {
		t.Fatal("TCP dial did not return TCP connection")
	}
	t.Cleanup(func() { _ = tcp.Close() })
	select {
	case server := <-accepted:
		t.Cleanup(func() { _ = server.Close() })
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("server did not accept loopback connection")
	}
	return tcp
}
