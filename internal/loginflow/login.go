// Package loginflow connects the reconstructed QR layers. It never requests an
// online QR renderer or submits credentials to a third-party service.
package loginflow

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/hybrid"
	"wechat-bot/internal/loginwire"
	"wechat-bot/internal/mmtls"
	"wechat-bot/internal/wechatnet"
)

const Profile = "wechat-ipad-20260913-experimental"
const ClientVersion = uint32(402668320)
const DeviceType = "iPad Air iPadOS18.4.1"

// Source: Client.Init iPad branch in the pinned ELF, login-static-profile.json.
const BootstrapHex = "047ebe7604acf072b0ab0177ea551a7b72588f9b5d3801dfd7bb1bca8e33d1c3b8fa6e4e4026eb38d5bb365088a3d3167c83bdd0bbb46255f88a16ede6f7ab43b5"

// WireProfile is the profile tuple used in every pre-authentication packet.
// A candidate must come from one coherent, audited artifact; changing only
// Version is deliberately not sufficient evidence that a profile will work.
// The zero value preserves checkpoints created before profiles were explicit.
type WireProfile struct {
	Version      uint32 `json:"version"`
	DeviceType   string `json:"device_type"`
	BootstrapHex string `json:"bootstrap_public_key_hex"`
}

func DefaultWireProfile() WireProfile {
	return WireProfile{Version: ClientVersion, DeviceType: DeviceType, BootstrapHex: BootstrapHex}
}

func (p WireProfile) Validate() error {
	if p.Version == 0 || p.Version > uint32(^uint32(0)>>1) || len(p.DeviceType) == 0 || len(p.DeviceType) > 256 || !utf8.ValidString(p.DeviceType) || strings.IndexByte(p.DeviceType, 0) >= 0 {
		return errors.New("invalid wire profile version or device type")
	}
	key, err := hex.DecodeString(p.BootstrapHex)
	if err != nil || len(key) != 65 || key[0] != 4 {
		return errors.New("invalid wire profile bootstrap public key")
	}
	if _, err = ecdh.P256().NewPublicKey(key); err != nil {
		return errors.New("wire profile bootstrap key is not a P-256 point")
	}
	return nil
}

func (p WireProfile) isZero() bool {
	return p.Version == 0 && p.DeviceType == "" && p.BootstrapHex == ""
}

type Carrier interface {
	Handshake(context.Context) (mmtls.Session, error)
	Exchange(context.Context, mmtls.Session, string, []byte) ([]byte, error)
}

// Pending contains login secrets. Store only locally with restricted access.
// Its JSON format is a work-in-progress checkpoint, not a completed send session.
type Pending struct {
	Profile                  string        `json:"profile"`
	Host                     string        `json:"host"`
	DeviceID                 []byte        `json:"device_id"`
	RandomKey                []byte        `json:"random_key"`
	NotifyKey                []byte        `json:"notify_key"`
	Cookie                   []byte        `json:"cookie"`
	UUID                     string        `json:"uuid"`
	CreatedAt                int64         `json:"created_at"`
	ExpiresAt                int64         `json:"expires_at"`
	MMTLS                    mmtls.Session `json:"mmtls"`
	LastStatus               *uint32       `json:"last_status,omitempty"`
	UserName                 string        `json:"username,omitempty"`
	TemporaryPassword        string        `json:"temporary_password,omitempty"`
	AuthorizationAttemptedAt int64         `json:"authorization_attempted_at,omitempty"`
	AuthorizationReturnCode  *int32        `json:"authorization_return_code,omitempty"`
	DeviceProfilePath        string        `json:"device_profile_path,omitempty"`
	WireProfile              *WireProfile  `json:"wire_profile,omitempty"`
}

func (p *Pending) wireProfile() (WireProfile, error) {
	if p == nil || p.WireProfile == nil || p.WireProfile.isZero() {
		return DefaultWireProfile(), nil
	}
	profile := *p.WireProfile
	if err := profile.Validate(); err != nil {
		return WireProfile{}, err
	}
	return profile, nil
}

// GetQR attempts one QR request. Device ID is generated once by the caller and
// reused; this layer deliberately does not generate a new identity per retry.
func GetQR(ctx context.Context, carrier Carrier, host string, deviceID []byte) (Pending, []byte, error) {
	return GetQRWithProfile(ctx, carrier, host, deviceID, DefaultWireProfile())
}

// GetQRWithProfile creates one QR request with the caller-selected, complete
// tuple. It does not accept a lone version override.
func GetQRWithProfile(ctx context.Context, carrier Carrier, host string, deviceID []byte, profile WireProfile) (Pending, []byte, error) {
	if carrier == nil || !wechatnet.ValidHost(host) || len(deviceID) != 16 || deviceID[0] != 0x49 {
		return Pending{}, nil, errors.New("invalid QR device or host")
	}
	if err := profile.Validate(); err != nil {
		return Pending{}, nil, err
	}
	session, err := carrier.Handshake(ctx)
	if err != nil {
		return Pending{}, nil, fmt.Errorf("initial handshake: %w", err)
	}
	key := make([]byte, 16)
	if _, err = rand.Read(key); err != nil {
		return Pending{}, nil, err
	}
	base := baseRequest(profile, deviceID, nil)
	opcode := uint32(0)
	deviceName := "iPad Air (第7代)"
	payload, err := loginwire.BuildQRRequest(base, key, loginwire.QRRequestOptions{Opcode: &opcode, DeviceName: &deviceName})
	if err != nil {
		return Pending{}, nil, err
	}
	x, err := newHybrid(profile)
	if err != nil {
		return Pending{}, nil, err
	}
	packet, err := x.Encode(502, 0, nil, payload)
	if err != nil {
		return Pending{}, nil, fmt.Errorf("QR encryption: %w", err)
	}
	response, err := carrier.Exchange(ctx, session, "/cgi-bin/micromsg-bin/getloginqrcode", packet)
	if err != nil {
		return Pending{}, nil, fmt.Errorf("QR MMTLS exchange: %w", err)
	}
	plain, cookie, err := x.Decode(response)
	if err != nil {
		return Pending{}, nil, fmt.Errorf("QR HybridECDH response: %w", err)
	}
	qr, err := loginwire.ParseQRResponse(plain)
	if err != nil {
		return Pending{}, nil, fmt.Errorf("QR protobuf: %w", err)
	}
	ret, err := qr.Code()
	if err != nil {
		return Pending{}, nil, err
	}
	if ret != 0 {
		return Pending{}, nil, fmt.Errorf("QR rejected, return code %d", ret)
	}
	if qr.UUID == nil || *qr.UUID == "" || qr.QRCode == nil || !qr.QRCode.HasData || len(qr.QRCode.Data) == 0 || qr.NotifyKey == nil || !qr.NotifyKey.HasData || len(qr.NotifyKey.Data) != 16 {
		return Pending{}, nil, errors.New("QR response lacks required login material")
	}
	now := time.Now().Unix()
	expires := now + 300
	// Historical response uses an absolute expiry. Treat a plausible TTL as a
	// TTL, and cap a server-provided deadline so old files cannot live forever.
	if qr.ExpiredTime != nil {
		n := int64(*qr.ExpiredTime)
		if n > now && n < now+900 {
			expires = n
		} else if n > 0 && n < 900 {
			expires = now + n
		}
	}
	p := Pending{Profile: Profile, Host: host, DeviceID: append([]byte(nil), deviceID...), RandomKey: key, NotifyKey: append([]byte(nil), qr.NotifyKey.Data...), Cookie: cookie, UUID: *qr.UUID, CreatedAt: now, ExpiresAt: expires, MMTLS: session, WireProfile: &profile}
	return p, append([]byte(nil), qr.QRCode.Data...), nil
}

func Poll(ctx context.Context, carrier Carrier, p *Pending) (uint32, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	profile, err := p.wireProfile()
	if err != nil {
		return 0, err
	}
	opcode := uint32(0)
	payload, err := loginwire.BuildPollRequest(baseRequest(profile, p.DeviceID, p.RandomKey), p.RandomKey, p.UUID, uint32(time.Now().Unix()), loginwire.PollRequestOptions{Opcode: &opcode})
	if err != nil {
		return 0, err
	}
	x, err := newHybrid(profile)
	if err != nil {
		return 0, err
	}
	packet, err := x.Encode(503, 0, p.Cookie, payload)
	if err != nil {
		return 0, err
	}
	response, err := carrier.Exchange(ctx, p.MMTLS, "/cgi-bin/micromsg-bin/checkloginqrcode", packet)
	if err != nil {
		return 0, fmt.Errorf("poll exchange: %w", err)
	}
	plain, cookie, err := x.Decode(response)
	if err != nil {
		return 0, fmt.Errorf("poll HybridECDH response: %w", err)
	}
	r, err := loginwire.ParsePollResponse(plain)
	if err != nil {
		return 0, err
	}
	ret, err := r.Code()
	if err != nil {
		return 0, err
	}
	if ret != 0 {
		return 0, fmt.Errorf("poll rejected, return code %d", ret)
	}
	if r.NotifyPackage == nil || r.NotifyPackage.NotifyData == nil || !r.NotifyPackage.NotifyData.HasData {
		return 0, errors.New("missing encrypted QR notification")
	}
	decoded, err := decryptNotify(r.NotifyPackage.NotifyData.Data, p.NotifyKey)
	if err != nil {
		return 0, err
	}
	notify, err := loginwire.ParseNotify(decoded)
	if err != nil {
		return 0, err
	}
	if notify.Status == nil {
		return 0, errors.New("QR notification has no status")
	}
	if notify.UUID != nil && *notify.UUID != "" && *notify.UUID != p.UUID {
		return 0, errors.New("QR notification does not match pending login")
	}
	p.LastStatus = notify.Status
	if len(cookie) > 0 {
		p.Cookie = append([]byte(nil), cookie...)
	}
	if *notify.Status == 2 {
		if notify.UserName == nil || notify.Password == nil || *notify.UserName == "" || *notify.Password == "" {
			return 0, errors.New("confirmed scan lacks temporary authorization material")
		}
		p.UserName = *notify.UserName
		p.TemporaryPassword = *notify.Password
	}
	return *notify.Status, nil
}

func (p *Pending) Validate() error {
	if p == nil || p.Profile != Profile || !wechatnet.ValidHost(p.Host) || len(p.DeviceID) != 16 || p.DeviceID[0] != 0x49 || len(p.RandomKey) != 16 || len(p.NotifyKey) != 16 || len(p.UUID) == 0 || len(p.UUID) > 256 || len(p.MMTLS.PSK) != 32 || len(p.MMTLS.Ticket) < 5 {
		return errors.New("invalid pending login state")
	}
	if _, err := p.wireProfile(); err != nil {
		return err
	}
	if time.Now().Unix() >= p.ExpiresAt {
		return errors.New("QR login has expired")
	}
	return nil
}
func baseRequest(profile WireProfile, deviceID, key []byte) loginwire.BaseRequest {
	uin, scene := uint32(0), uint32(0)
	version := int32(profile.Version)
	return loginwire.BaseRequest{SessionKey: key, HasSessionKey: true, UIN: &uin, DeviceID: deviceID, HasDeviceID: true, ClientVersion: &version, DeviceType: []byte(profile.DeviceType), HasDeviceType: true, Scene: &scene}
}
func newHybrid(profile WireProfile) (*hybrid.Client, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(profile.BootstrapHex)
	if err != nil {
		return nil, err
	}
	return hybrid.New(key, profile.Version, rand.Reader)
}
func decryptNotify(b, key []byte) ([]byte, error) {
	if len(key) != 16 || len(b) == 0 || len(b) > 2<<20 || len(b)%aes.BlockSize != 0 {
		return nil, errors.New("invalid QR notification ciphertext")
	}
	block, _ := aes.NewCipher(key)
	plain := make([]byte, len(b))
	cipher.NewCBCDecrypter(block, key).CryptBlocks(plain, b)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(plain) || !bytes.Equal(plain[len(plain)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("invalid QR notification padding")
	}
	return plain[:len(plain)-pad], nil
}
