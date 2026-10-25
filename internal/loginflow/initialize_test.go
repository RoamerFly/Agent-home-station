package loginflow

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"wechat-bot/internal/business"
	"wechat-bot/internal/mmtls"
	"wechat-bot/internal/native"
)

type syntheticInitCarrier struct {
	t       *testing.T
	session business.Session
	calls   int
	fail    bool
	marker  *bool
}

func (*syntheticInitCarrier) Handshake(context.Context) (mmtls.Session, error) {
	return mmtls.Session{}, errors.New("unexpected handshake")
}
func (c *syntheticInitCarrier) Exchange(_ context.Context, _ mmtls.Session, path string, packet []byte) ([]byte, error) {
	c.calls++
	if c.marker != nil && !*c.marker {
		c.t.Fatal("request left before durable marker")
	}
	if path != "/cgi-bin/micromsg-bin/newinit" {
		c.t.Fatal("wrong CGI path")
	}
	cgi, _ := binary.Uvarint(packet[26:])
	if cgi != 139 {
		c.t.Fatal("wrong CGI framing")
	}
	if _, _, err := business.Decode(packet, c.session); err != nil {
		c.t.Fatal(err)
	}
	if c.fail {
		return nil, errors.New("synthetic lost connection")
	}
	raw, err := os.ReadFile("../initwire/testdata/newinit-response.pb.hex")
	if err != nil {
		c.t.Fatal(err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		c.t.Fatal(err)
	}
	return business.EncodeOperation(b, c.session, business.InitCGI, func([]byte) (uint32, error) { return 1, nil })
}
func syntheticInitSession() native.SessionFile {
	return native.SessionFile{Profile: Profile, Experimental: true, ShortHost: "short.weixin.qq.com", ClientVersion: ClientVersion, UIN: 7, SessionKey: bytes.Repeat([]byte{1}, 16), Cookie: bytes.Repeat([]byte{2}, 15), LoginSalt: []byte("synthetic-salt"), UserName: "synthetic-user", DeviceID: append([]byte{0x49}, make([]byte, 15)...), DeviceType: DeviceType}
}
func initTestCarrier(t *testing.T, s native.SessionFile) *syntheticInitCarrier {
	return &syntheticInitCarrier{t: t, session: business.Session{ClientVersion: s.ClientVersion, UIN: s.UIN, SessionKey: s.SessionKey, Cookie: s.Cookie, LoginSalt: s.LoginSalt}}
}

func TestInitializationPreservesSyncAndDoesNotLoopOnContinueFlag(t *testing.T) {
	s := syntheticInitSession()
	c := initTestCarrier(t, s)
	marked := false
	c.marker = &marked
	saves := 0
	recoverySaved := false
	r, err := InitializePage(context.Background(), c, &s, InitializationOptions{Checkpoint: func(state *native.SessionFile) error { saves++; marked = state.InitializationInFlight; return nil }, ResponseCheckpoint: func(b []byte) error { recoverySaved = len(b) > 0; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Verified || r.ContinueFlag == nil || *r.ContinueFlag != 1 || r.Commands != 1 || c.calls != 1 || saves != 2 || !recoverySaved || s.InitializationInFlight || !s.InitializationVerified || len(s.CurrentSyncKey) == 0 {
		t.Fatal("verified page or persistence state incorrect")
	}
}
func TestInitializationUnknownOutcomeCannotResubmit(t *testing.T) {
	s := syntheticInitSession()
	c := initTestCarrier(t, s)
	c.fail = true
	if _, err := InitializePage(context.Background(), c, &s, InitializationOptions{}); err == nil || !s.InitializationInFlight {
		t.Fatal("lost outcome not retained")
	}
	if _, err := InitializePage(context.Background(), c, &s, InitializationOptions{}); err == nil || c.calls != 1 {
		t.Fatal("unknown operation was resubmitted")
	}
}
