package authwire

import (
	"bytes"
	"os"
	"testing"
)

func TestExtInfoV8MatchesIndependentProtobuf(t *testing.T) {
	want, err := os.ReadFile("testdata/extinfo-v8.pb")
	if err != nil {
		t.Fatal(err)
	}
	got, err := BuildExtInfoV8(ExtInfoV8Parts{WCSTF: []byte("synthetic-wcstf"), WCSTE: []byte("synthetic-wcste"), EncryptedDeviceData: []byte("synthetic-encrypted-device-data"), DeviceToken: "synthetic-token", CryptoTimestamp: 123456, TokenTimestamp: 123457})
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("independent protobuf mismatch: %v", err)
	}
}

func TestExtInfoV8RejectsMissingProducerOutput(t *testing.T) {
	if _, err := BuildExtInfoV8(ExtInfoV8Parts{}); err == nil {
		t.Fatal("missing producers accepted")
	}
}
