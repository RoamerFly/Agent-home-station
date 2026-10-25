package loginflow

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"

	"wechat-bot/internal/loginwire"
	"wechat-bot/internal/mmtls"
	"wechat-bot/internal/sae08"
)

func syntheticFingerprint() FingerprintInput {
	return FingerprintInput{DeviceID: "synthetic-device", Attributes: []loginwire.DeviceAttribute{{Key: "deviceid", Value: "synthetic-device"}, {Key: "empty", Value: ""}}, SessionKey: []byte("0123456789abcdef"), Timestamp: 123456}
}

func frozenFingerprintCipher(t *testing.T) *sae08.Cipher {
	t.Helper()
	b, err := os.ReadFile("../../research/samples/08sae.dat.bin")
	if err != nil {
		t.Fatal(err)
	}
	c, err := sae08.ParsePinned(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Read only length-delimited fields for this envelope assertion. Crypto and
// full protobuf byte encoding have independent model/runtime tests elsewhere.
func envelopeFields(t *testing.T, b []byte) map[uint64][]byte {
	t.Helper()
	out := map[uint64][]byte{}
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			t.Fatal("invalid envelope tag")
		}
		b = b[n:]
		if tag&7 == 0 {
			_, n = binary.Uvarint(b)
			if n <= 0 {
				t.Fatal("invalid scalar")
			}
			b = b[n:]
			continue
		}
		if tag&7 != 2 {
			t.Fatal("unexpected envelope wire type")
		}
		length, n := binary.Uvarint(b)
		if n <= 0 || length > uint64(len(b)-n) {
			t.Fatal("invalid envelope length")
		}
		b = b[n:]
		out[tag>>3] = b[:length]
		b = b[length:]
	}
	return out
}

func TestFingerprintPreparationPreservesDeviceAndSession(t *testing.T) {
	input := syntheticFingerprint()
	payload, err := PrepareFingerprint(frozenFingerprintCipher(t), input)
	if err != nil {
		t.Fatal(err)
	}
	f := envelopeFields(t, payload)
	if string(f[2]) != string(input.SessionKey) {
		t.Fatal("session key changed")
	}
	base := envelopeFields(t, f[1])
	if string(base[3]) != input.DeviceID+"\x00" {
		t.Fatal("textual device ID must end with NUL")
	}
	zt := envelopeFields(t, f[3])
	if string(zt[1]) != "00000008\x00" || len(zt[3]) == 0 || len(zt[3])%16 != 0 {
		t.Fatal("invalid ZT envelope")
	}
}

type fingerprintLostCarrier struct {
	calls  int
	path   string
	packet []byte
}

func (*fingerprintLostCarrier) Handshake(context.Context) (mmtls.Session, error) {
	return mmtls.Session{}, errors.New("unexpected handshake")
}
func (c *fingerprintLostCarrier) Exchange(_ context.Context, _ mmtls.Session, path string, packet []byte) ([]byte, error) {
	c.calls++
	c.path = path
	c.packet = append([]byte(nil), packet...)
	return nil, errors.New("synthetic lost connection")
}

func TestFingerprintExchangeDoesNotRetry(t *testing.T) {
	c := &fingerprintLostCarrier{}
	_, err := FetchDeviceToken(context.Background(), c, mmtls.Session{}, frozenFingerprintCipher(t), syntheticFingerprint())
	if err == nil || !strings.Contains(err.Error(), "not retried") || c.calls != 1 || c.path != "/cgi-bin/micromsg-bin/fpinitnl" {
		t.Fatalf("unexpected exchange state: err=%v calls=%d path=%s", err, c.calls, c.path)
	}
}

func TestFingerprintExchangeCarriesSelectedWireProfile(t *testing.T) {
	selected := DefaultWireProfile()
	selected.Version++
	input := syntheticFingerprint()
	input.WireProfile = &selected
	c := &fingerprintLostCarrier{}
	_, err := FetchDeviceToken(context.Background(), c, mmtls.Session{}, frozenFingerprintCipher(t), input)
	if err == nil || c.calls != 1 || len(c.packet) < 7 {
		t.Fatalf("custom profile request was not attempted: err=%v calls=%d bytes=%d", err, c.calls, len(c.packet))
	}
	if got := binary.BigEndian.Uint32(c.packet[3:7]); got != selected.Version {
		t.Fatalf("outer request used version %x, want selected %x", got, selected.Version)
	}
}
