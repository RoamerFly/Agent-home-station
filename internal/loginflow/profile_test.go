package loginflow

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"wechat-bot/internal/mmtls"
)

func TestPendingProfileDefaultsAndValidatesTuple(t *testing.T) {
	legacy := &Pending{}
	got, err := legacy.wireProfile()
	if err != nil || got != DefaultWireProfile() {
		t.Fatalf("legacy checkpoint did not resolve default: got=%+v err=%v", got, err)
	}
	broken := DefaultWireProfile()
	broken.BootstrapHex = "not-a-key"
	legacy.WireProfile = &broken
	if _, err = legacy.wireProfile(); err == nil {
		t.Fatal("incomplete profile tuple accepted")
	}
}

func TestWireProfileRejectsUnsignedVersionOverflow(t *testing.T) {
	p := DefaultWireProfile()
	p.Version = 0x80000000
	if err := p.Validate(); err == nil {
		t.Fatal("version that cannot fit protocol int32 accepted")
	}
}

type versionCaptureCarrier struct {
	packet []byte
	path   string
	calls  int
}

func (*versionCaptureCarrier) Handshake(context.Context) (mmtls.Session, error) {
	return mmtls.Session{PSK: make([]byte, 32), Ticket: make([]byte, 5)}, nil
}
func (c *versionCaptureCarrier) Exchange(_ context.Context, _ mmtls.Session, path string, packet []byte) ([]byte, error) {
	c.calls++
	c.path = path
	c.packet = append([]byte(nil), packet...)
	return nil, errors.New("offline capture complete; request was not transmitted")
}

// Inspect the bytes delivered to the network boundary, rather than just a
// configuration variable. This rules out falling back to the old header version
// in any of the three account-login operations after a profile migration.
func TestAllLoginOperationsCarryUpgradedHeaderVersion(t *testing.T) {
	for _, operation := range []string{"qr", "poll", "authorize"} {
		t.Run(operation, func(t *testing.T) {
			wire := DefaultWireProfile()
			wire.Version = 402669099 // observed candidate; no acceptance claim.
			p := syntheticConfirmedPending()
			p.WireProfile = &wire
			c := &versionCaptureCarrier{}
			var err error
			switch operation {
			case "qr":
				_, _, err = GetQRWithProfile(context.Background(), c, p.Host, p.DeviceID, wire)
			case "poll":
				_, err = Poll(context.Background(), c, &p)
			case "authorize":
				_, err = Authorize(context.Background(), c, &p, AuthorizationOptions{})
			}
			if err == nil || c.calls != 1 || len(c.packet) < 7 {
				t.Fatalf("operation did not reach offline boundary exactly once: err=%v calls=%d", err, c.calls)
			}
			if got := binary.BigEndian.Uint32(c.packet[3:7]); got != wire.Version {
				t.Fatalf("%s transmitted version %x, want %x", c.path, got, wire.Version)
			}
		})
	}
}
