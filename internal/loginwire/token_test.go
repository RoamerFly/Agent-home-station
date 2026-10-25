package loginwire

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func tokenFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".pb")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestTrustRequestIndependentEncoding(t *testing.T) {
	si, md := "synthetic-si", "synthetic-md"
	got, err := BuildTrustRequest([]DeviceAttribute{{"deviceid", "synthetic-device"}, {"empty", ""}}, &si, &md)
	if err != nil || !bytes.Equal(got, tokenFixture(t, "trust-request")) {
		t.Fatalf("independent encoding mismatch: %v", err)
	}
	if _, err = BuildTrustRequest([]DeviceAttribute{{"deviceid", "a"}, {"deviceid", "b"}}, nil, nil); err == nil {
		t.Fatal("duplicate identity accepted")
	}
}
func TestFingerprintIndependentEncoding(t *testing.T) {
	zero, one, five, stamp := uint32(0), uint32(1), uint32(5), uint32(123456)
	version := int32(402668320)
	ztVersion := "00000008\x00"
	b := BaseRequest{HasSessionKey: true, UIN: &zero, DeviceID: []byte("synthetic-id\x00"), HasDeviceID: true, ClientVersion: &version, DeviceType: []byte("iPad Air iPadOS18.4.1"), HasDeviceType: true, Scene: &zero}
	got, err := BuildFingerprintRequest(b, []byte("0123456789abcdef"), ZTData{Version: &ztVersion, Encrypted: &one, Data: []byte("synthetic-encrypted-trust"), Timestamp: &stamp, Operation: &five, UIN: &zero})
	if err != nil || !bytes.Equal(got, tokenFixture(t, "fingerprint-request")) {
		t.Fatalf("independent encoding mismatch: %v", err)
	}
}
func TestTrustResponsePresenceAndSecret(t *testing.T) {
	r, err := ParseTrustResponse(tokenFixture(t, "trust-response"))
	if err != nil {
		t.Fatal(err)
	}
	code, err := r.Code()
	if err != nil || code != 0 || !r.HasData || r.Token == nil || string(*r.Token) != "synthetic-device-token" || r.Timestamp == nil || *r.Timestamp != 123457 || len(r.SoftData) == 0 {
		t.Fatal("missing independent response fields")
	}
	if fmt.Sprint(*r.Token) != "[redacted]" {
		t.Fatal("token leaked through fmt")
	}
	if r.SoftConfig == nil || string(*r.SoftConfig) != "synthetic-config" || !r.HasSoftPayload || string(r.SoftPayload) != "synthetic-soft-data" {
		t.Fatal("soft data semantic fields lost")
	}
	r, err = ParseTrustResponse([]byte{0x12, 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Code(); err == nil {
		t.Fatal("absent ret treated as success")
	}
	if _, err = ParseTrustResponse([]byte{0x12, 10, 0x1a, 1, 'x'}); err == nil {
		t.Fatal("truncation accepted")
	}
}
