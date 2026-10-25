package loginflow

import (
	"bytes"
	"math/rand"
	"testing"
	"time"

	"wechat-bot/internal/deviceprofile"
)

func TestAuthorizationProofBuildsAllNativeContainerInputs(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 123456000, time.UTC)
	p, err := deviceprofile.Generate(deviceprofile.GenerateOptions{LoginDeviceID: "490102030405060708090a0b0c0d0e0f", Now: now, Location: time.UTC, UUIDEntropy: bytes.NewReader(bytes.Repeat([]byte{0x22}, 64)), Random: rand.New(rand.NewSource(77))})
	if err != nil {
		t.Fatal(err)
	}
	clockCalls := 0
	clock := func() time.Time { clockCalls++; return now }
	proof, err := AuthorizationProof(p, "synthetic-token", "synthetic-user", frozenFingerprintCipher(t), clock, rand.New(rand.NewSource(33)))
	if err != nil {
		t.Fatal(err)
	}
	f := envelopeFields(t, proof)
	if len(f) != 4 || len(f[1]) == 0 || len(f[2]) == 0 || len(f[3]) == 0 || len(f[7]) == 0 || clockCalls < 5 {
		t.Fatal("recovered WCExtInfo inputs or time samples missing")
	}
	// DeviceToken and CryptoData are separately wrapped protobufs. Do not
	// substitute a soft-data payload for the actual token text.
	token := envelopeFields(t, envelopeFields(t, f[7])[2])
	if string(envelopeFields(t, token[3])[1]) != "synthetic-token" {
		t.Fatal("token container lost original token")
	}
	crypto := envelopeFields(t, envelopeFields(t, f[3])[2])
	if string(crypto[1]) != "00000008" || len(crypto[3]) == 0 {
		t.Fatal("missing NewClientCheckData running container")
	}
}

func TestAuthorizationProofRejectsMissingServerMaterial(t *testing.T) {
	if _, err := AuthorizationProof(deviceprofile.Profile{}, "", "user", nil, time.Now, rand.New(rand.NewSource(1))); err == nil {
		t.Fatal("missing token and profile accepted")
	}
}
