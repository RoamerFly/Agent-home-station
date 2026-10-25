package loginflow

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"wechat-bot/internal/deviceprofile"
	"wechat-bot/internal/hybrid"
	"wechat-bot/internal/loginwire"
	"wechat-bot/internal/mmtls"
	"wechat-bot/internal/sae08"
	"wechat-bot/internal/v08"
)

// FingerprintInput is an explicit, persisted device profile plus request inputs.
// This type does not generate attributes, hardware claims or a device token.
// The profile-generation/dataflow layer is still being reconstructed.
type FingerprintInput struct {
	DeviceID   string
	Attributes []loginwire.DeviceAttribute
	SI, MD     *string
	SessionKey []byte
	Timestamp  uint32
	// WireProfile must match the profile used for QR/poll/authorization. A nil
	// pointer is retained for existing offline callers and selects the default.
	WireProfile *WireProfile
}

// FingerprintFromProfile uses the same persisted profile for attribute values
// and the outer textual device ID. Random session key and timestamp remain
// explicit inputs so callers cannot silently generate a new identity.
func FingerprintFromProfile(profile deviceprofile.Profile, sessionKey []byte, timestamp uint32) (FingerprintInput, error) {
	attributes, err := profile.FingerprintAttributes()
	if err != nil {
		return FingerprintInput{}, err
	}
	if len(sessionKey) != 16 {
		return FingerprintInput{}, errors.New("fingerprint session key must be 16 bytes")
	}
	// FpInitAndRrefresh calls SendIosDeviceTokenRequest with an empty SI
	// string, whose pointer is still present in the TrustData message.
	emptySI := ""
	if profile.DeviceType != DeviceType {
		return FingerprintInput{}, errors.New("fingerprint profile device type is not recovered")
	}
	w := DefaultWireProfile()
	w.Version = profile.ClientVersion
	return FingerprintInput{DeviceID: profile.DeviceID, Attributes: attributes, SI: &emptySI, SessionKey: append([]byte(nil), sessionKey...), Timestamp: timestamp, WireProfile: &w}, nil
}

// PrepareFingerprint reconstructs the static SendIosDeviceTokenRequest chain:
// TrustReq -> default Go zlib -> ZT full-padding transform -> FPFresh.
// It performs no network calls and does not treat prepared bytes as a token.
func PrepareFingerprint(c *sae08.Cipher, in FingerprintInput) ([]byte, error) {
	if c == nil || len(in.DeviceID) == 0 || len(in.DeviceID) > 256 || !utf8.ValidString(in.DeviceID) || len(in.SessionKey) != 16 {
		return nil, errors.New("invalid fingerprint device or encryption inputs")
	}
	plain, err := loginwire.BuildTrustRequest(in.Attributes, in.SI, in.MD)
	if err != nil {
		return nil, err
	}
	compressed, err := v08.Compress(plain)
	if err != nil {
		return nil, err
	}
	encrypted, err := c.EncryptZT(compressed)
	if err != nil {
		return nil, err
	}
	// Native SendIosDeviceTokenRequest appends NUL to the textual device ID.
	profile := DefaultWireProfile()
	if in.WireProfile != nil {
		profile = *in.WireProfile
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	base := baseRequest(profile, append([]byte(in.DeviceID), 0), nil)
	zero, one, five := uint32(0), uint32(1), uint32(5)
	version := "00000008\x00"
	return loginwire.BuildFingerprintRequest(base, in.SessionKey, loginwire.ZTData{Version: &version, Encrypted: &one, Data: encrypted, Timestamp: &in.Timestamp, Operation: &five, UIN: &zero})
}

// FetchDeviceToken performs one fpinitnl exchange using an explicitly supplied
// full profile. Callers must persist the returned material privately. There is
// no retry and no account authorization or message transmission in this step.
func FetchDeviceToken(ctx context.Context, carrier Carrier, session mmtls.Session, cipher *sae08.Cipher, in FingerprintInput) (loginwire.TrustResponse, error) {
	var empty loginwire.TrustResponse
	if carrier == nil {
		return empty, errors.New("missing fingerprint carrier")
	}
	payload, err := PrepareFingerprint(cipher, in)
	if err != nil {
		return empty, err
	}
	profile := DefaultWireProfile()
	if in.WireProfile != nil {
		profile = *in.WireProfile
	}
	x, err := newHybrid(profile)
	if err != nil {
		return empty, err
	}
	packet, err := x.Encode(hybrid.CGIFingerprint, 0, nil, payload)
	if err != nil {
		return empty, err
	}
	raw, err := carrier.Exchange(ctx, session, "/cgi-bin/micromsg-bin/fpinitnl", packet)
	if err != nil {
		return empty, fmt.Errorf("fingerprint exchange failed; not retried: %w", err)
	}
	plain, _, err := x.Decode(raw)
	if err != nil {
		return empty, fmt.Errorf("fingerprint HybridECDH response: %w", err)
	}
	r, err := loginwire.ParseTrustResponse(plain)
	if err != nil {
		return empty, err
	}
	code, err := r.Code()
	if err != nil {
		return empty, err
	}
	if code != 0 {
		return empty, fmt.Errorf("fingerprint rejected, return code %d", code)
	}
	if !r.HasData || r.Token == nil || len(*r.Token) == 0 || r.Timestamp == nil || r.SoftConfig == nil || !r.HasSoftPayload || len(r.SoftPayload) == 0 {
		return empty, errors.New("fingerprint response lacks device token material")
	}
	return r, nil
}
