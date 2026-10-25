package loginwire

import (
	"bytes"
	"errors"
	"testing"
)

func pU32(v uint32) *uint32    { return &v }
func pI32(v int32) *int32      { return &v }
func pString(v string) *string { return &v }

func TestBuildQRAndPollRequests(t *testing.T) {
	base := BaseRequest{SessionKey: []byte{}, HasSessionKey: true, UIN: pU32(0), DeviceID: []byte{1, 2}, HasDeviceID: true, ClientVersion: pI32(7), DeviceType: []byte("iPad"), HasDeviceType: true, Scene: pU32(0)}
	name := "test device"
	got, err := BuildQRRequest(base, []byte("1234567890123456"), QRRequestOptions{DeviceName: &name})
	if err != nil {
		t.Fatal(err)
	}
	// Independent field walk checks the descriptor field numbers and wire types.
	var fieldsSeen []int
	if err := parseFields(got, 0, func(f field) error { fieldsSeen = append(fieldsSeen, f.number); return nil }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte{1, 2, 4}, intsToBytes(fieldsSeen)) {
		t.Fatalf("QR fields %v", fieldsSeen)
	}
	poll, err := BuildPollRequest(base, []byte("1234567890123456"), "uuid", 99, PollRequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fieldsSeen = nil
	if err := parseFields(poll, 0, func(f field) error { fieldsSeen = append(fieldsSeen, f.number); return nil }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte{1, 2, 3, 4}, intsToBytes(fieldsSeen)) {
		t.Fatalf("poll fields %v", fieldsSeen)
	}
}

func intsToBytes(v []int) []byte {
	out := make([]byte, len(v))
	for i, n := range v {
		out[i] = byte(n)
	}
	return out
}

func TestParseQRResponsePresenceAndUnknown(t *testing.T) {
	// Descriptor-derived offline fixture: BaseResponse(ret=0,errMsg="ok"),
	// qrcode Buffer(len=3,"abc"), uuid="u1", notify key Buffer(len=4),
	// expiry=9, plus an unknown varint. It contains no live traffic or secrets.
	raw := []byte{0x0a, 0x08, 0x08, 0x00, 0x12, 0x04, 0x0a, 0x02, 'o', 'k', 0x12, 0x07, 0x08, 0x03, 0x12, 0x03, 'a', 'b', 'c', 0x1a, 0x02, 'u', '1', 0x2a, 0x08, 0x08, 0x04, 0x12, 0x04, 'k', 'e', 'y', '!', 0x30, 0x09, 0x98, 0x06, 0x01}
	r, err := ParseQRResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := r.Code(); err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if r.UUID == nil || *r.UUID != "u1" || r.QRCode == nil || !r.QRCode.HasData || string(r.QRCode.Data) != "abc" || r.NotifyKey == nil || r.ExpiredTime == nil || *r.ExpiredTime != 9 {
		t.Fatalf("unexpected QR response %#v", r)
	}
	if _, err := ParseQRResponse([]byte{0x1a, 0x01, 0xff}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("invalid UTF-8: %v", err)
	}
	missing, err := ParseQRResponse([]byte{0x1a, 0x01, 'x'})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.Code(); !errors.Is(err, ErrMissingField) {
		t.Fatalf("missing return code treated as success: %v", err)
	}
}

func TestPollAndNotifyLayering(t *testing.T) {
	// CheckLoginQRCodeResponse: base ret=0; encrypted NotifyPkg carries bytes.
	raw := []byte{0x0a, 0x02, 0x08, 0x00, 0x1a, 0x0b, 0x0a, 0x07, 0x08, 0x03, 0x12, 0x03, 'x', 'y', 'z', 0x10, 0x00}
	p, err := ParsePollResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Code(); err != nil || code != 0 {
		t.Fatalf("code %d, %v", code, err)
	}
	if p.NotifyPackage == nil || p.NotifyPackage.NotifyData == nil || string(p.NotifyPackage.NotifyData.Data) != "xyz" {
		t.Fatalf("outer package %#v", p)
	}
	// This is separately decrypted plaintext LoginQRCodeNotify, including an
	// explicit status=2 and temporary credentials used by manual auth.
	n := []byte{0x0a, 0x02, 'u', '1', 0x10, 0x02, 0x1a, 0x03, 'w', 'x', '1', 0x22, 0x02, 'p', 'w', 0x3a, 0x02, 'n', 'n'}
	notify, err := ParseNotify(n)
	if err != nil {
		t.Fatal(err)
	}
	if notify.Status == nil || *notify.Status != 2 || notify.UserName == nil || *notify.UserName != "wx1" || notify.Password == nil || *notify.Password != "pw" || notify.NickName == nil {
		t.Fatalf("notify %#v", notify)
	}
}

func TestRejectLengthMismatchAndDeepMalformed(t *testing.T) {
	// SKBuiltinBuffer_t iLen says two, data has one byte.
	_, err := ParseQRResponse([]byte{0x12, 0x05, 0x08, 0x02, 0x12, 0x01, 'x'})
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("length mismatch err=%v", err)
	}
	_, err = BuildPollRequest(BaseRequest{}, []byte("key"), string([]byte{0xff}), 1, PollRequestOptions{})
	if !errors.Is(err, ErrMissingField) {
		t.Fatalf("invalid UUID err=%v", err)
	}
}
