package loginflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"time"

	"wechat-bot/internal/authwire"
	"wechat-bot/internal/deviceprofile"
	"wechat-bot/internal/native"
	"wechat-bot/internal/wechatnet"
)

// AuthorizationOptions accepts actual recovered proof bytes. Omitting them
// leaves the fields absent and may be rejected by the server; this implementation
// does not invent device tokens or silently fetch them from a third party.
type AuthorizationOptions struct {
	ClientCheckData []byte
	ExtSpamInfo     []byte
	DeviceProfile   *deviceprofile.Profile
	// ResponseCheckpoint retains material privately before parsing, allowing
	// local recovery after a codec error without resubmitting authorization.
	ResponseCheckpoint func(AuthorizationExchange) error
	// Checkpoint persists the attempted marker before the request can leave.
	// A persistence error prevents the exchange. In-memory callers may omit it.
	Checkpoint func(*Pending) error
}

// AuthorizationExchange is sensitive local recovery data. Never log or upload
// it. It is not itself a validated account session.
type AuthorizationExchange struct {
	Profile                                                  string `json:"profile"`
	ReceivedAt                                               int64  `json:"received_at"`
	ClientPrivate, ClientPublic, RandomKey, Cookie, Response []byte
}

// AuthorizationRejected is a decoded server refusal, not a network failure.
// The code alone does not establish whether the account is restricted.
type AuthorizationRejected struct {
	Code            int32
	UpgradeRequired bool
}

func (e *AuthorizationRejected) Error() string {
	if e.UpgradeRequired {
		return fmt.Sprintf("authorization rejected: %d; server requires a newer application version; no account session created", e.Code)
	}
	if e.Code == -106 {
		return "authorization rejected: -106 (MM_ERR_BLOCK_BY_SPAM); no account session created"
	}
	return fmt.Sprintf("authorization rejected, return code %d", e.Code)
}

// Recognize a specific server instruction without logging arbitrary response
// text, URLs or identifiers. Other -106 refusals remain unclassified.
func serverRequiresUpgrade(message *string) bool {
	if message == nil || len(*message) > 16384 {
		return false
	}
	var envelope struct {
		Content string `xml:"Content"`
	}
	if err := xml.Unmarshal([]byte(*message), &envelope); err != nil {
		return false
	}
	return strings.Contains(envelope.Content, "应用版本过低") && strings.Contains(envelope.Content, "升级")
}

// Authorize performs exactly one authorization request after a confirmed scan.
// There is no retry on a network error: the server may already have acted on it.
// The returned format is for our native sender, not a promise of account health.
func Authorize(ctx context.Context, carrier Carrier, p *Pending, opt AuthorizationOptions) (native.SessionFile, error) {
	if p != nil && p.AuthorizationAttemptedAt != 0 {
		return native.SessionFile{}, errors.New("authorization already attempted for this checkpoint; inspect the prior outcome before starting another login")
	}
	if err := p.Validate(); err != nil {
		return native.SessionFile{}, err
	}
	if p.LastStatus == nil || *p.LastStatus != 2 || p.UserName == "" || p.TemporaryPassword == "" {
		return native.SessionFile{}, errors.New("scan must be confirmed before authorization")
	}
	wire, err := p.wireProfile()
	if err != nil {
		return native.SessionFile{}, err
	}
	private, public, err := authwire.GenerateP224()
	if err != nil {
		return native.SessionFile{}, err
	}
	randomKey := make([]byte, 16)
	if _, err = rand.Read(randomKey); err != nil {
		return native.SessionFile{}, err
	}
	uin, scene, zero, input := uint32(0), uint32(1), uint32(0), uint32(2)
	channel := int32(0)
	version := int32(wire.Version)
	now := uint32(time.Now().Unix())
	name, kind, lang, zone, brand, country, bundle, model := deviceprofile.DeviceName, "iPad", "zh_CN", "8.0", "Apple", "CN", "com.tencent.xin", deviceprofile.Model
	deviceType := wire.DeviceType
	imei := hex.EncodeToString(p.DeviceID)
	osVersion, romModel := deviceprofile.OSVersion, deviceprofile.Model
	if opt.DeviceProfile != nil {
		if err := opt.DeviceProfile.ValidateFingerprint(); err != nil {
			return native.SessionFile{}, err
		}
		if opt.DeviceProfile.LoginDeviceID != imei || opt.DeviceProfile.ClientVersion != wire.Version || opt.DeviceProfile.DeviceType != wire.DeviceType {
			return native.SessionFile{}, errors.New("authorization profile differs from pending QR identity or version")
		}
		name = opt.DeviceProfile.DeviceName
		osVersion = opt.DeviceProfile.OSVersion
		romModel = opt.DeviceProfile.Model
		model = opt.DeviceProfile.Model
	}
	softType := deviceprofile.AuthSoftType(imei, osVersion, romModel)
	adSource, _ := deviceprofile.AuthIdentityPair(imei)
	seq := imei + "-" + fmt.Sprint(now)
	base := authwire.BaseRequest{SessionKey: randomKey, HasSessionKey: true, UIN: &uin, DeviceID: p.DeviceID, HasDeviceID: true, ClientVersion: &version, DeviceType: []byte(wire.DeviceType), HasDeviceType: true, Scene: &scene}
	device := authwire.DeviceFields{IMEI: &imei, SoftType: &softType, AdSource: &adSource, ClientSeqID: &seq, DeviceName: &name, DeviceType: &kind, Language: &lang, TimeZone: &zone, Channel: &channel, Timestamp: &now, DeviceBrand: &brand, OSType: &deviceType, RealCountry: &country, BundleID: &bundle, IPhoneVersion: &model, BuiltinIPSeq: &zero, InputType: &input}
	buffer := func(b []byte) *authwire.Buffer {
		n := uint32(len(b))
		return &authwire.Buffer{Length: &n, Data: b, HasData: true}
	}
	if len(opt.ClientCheckData) > 0 {
		device.ClientCheckData = buffer(opt.ClientCheckData)
	}
	if len(opt.ExtSpamInfo) > 0 {
		device.ExtSpamInfo = buffer(opt.ExtSpamInfo)
	}
	payload, err := authwire.BuildRequest(authwire.RequestInput{Base: base, BaseAuth: &authwire.BaseAuthReqInfo{}, RandomKey: randomKey, ClientECDHPublic: public, UserName: p.UserName, Password: authwire.Secret(p.TemporaryPassword), Device: device})
	if err != nil {
		return native.SessionFile{}, err
	}
	x, err := newHybrid(wire)
	if err != nil {
		return native.SessionFile{}, err
	}
	packet, err := x.Encode(252, 0, nil, payload)
	if err != nil {
		return native.SessionFile{}, err
	}
	p.AuthorizationAttemptedAt = time.Now().Unix()
	if opt.Checkpoint != nil {
		if err := opt.Checkpoint(p); err != nil {
			return native.SessionFile{}, errors.New("cannot persist authorization marker; request not sent")
		}
	}
	response, err := carrier.Exchange(ctx, p.MMTLS, "/cgi-bin/micromsg-bin/secmanualauth", packet)
	if err != nil {
		return native.SessionFile{}, fmt.Errorf("authorization exchange, outcome unknown: %w", err)
	}
	plain, cookie, err := x.Decode(response)
	if err != nil {
		return native.SessionFile{}, fmt.Errorf("authorization HybridECDH: %w", err)
	}
	if opt.ResponseCheckpoint != nil {
		if err = opt.ResponseCheckpoint(AuthorizationExchange{Profile: Profile, ReceivedAt: time.Now().Unix(), ClientPrivate: private, ClientPublic: public, RandomKey: randomKey, Cookie: cookie, Response: plain}); err != nil {
			return native.SessionFile{}, errors.New("authorization response received but private recovery checkpoint could not be saved; do not resubmit")
		}
	}
	r, err := authwire.ParseResponse(plain)
	if err != nil {
		return native.SessionFile{}, err
	}
	ret, err := r.Code()
	if err != nil {
		return native.SessionFile{}, err
	}
	if ret != 0 {
		p.AuthorizationReturnCode = &ret
		return native.SessionFile{}, &AuthorizationRejected{Code: ret, UpgradeRequired: serverRequiresUpgrade(r.Base.Error)}
	}
	if r.SectionFlag == nil || *r.SectionFlag == 0 || r.UIN == nil || *r.UIN == 0 || r.ServerECDH == nil || r.ServerECDH.Key == nil || r.EncryptedSessionKey == nil || !r.EncryptedSessionKey.HasData || r.Account == nil || r.Account.UserName == nil || *r.Account.UserName != p.UserName {
		return native.SessionFile{}, errors.New("authorization response lacks account session material")
	}
	key, err := authwire.DecryptSessionKey(private, r.ServerECDH.Key.Data, r.EncryptedSessionKey.Data)
	if err != nil {
		return native.SessionFile{}, fmt.Errorf("authorization session key: %w", err)
	}
	if len(key) != 16 || len(cookie) != 15 {
		return native.SessionFile{}, errors.New("authorization session length is unsupported")
	}
	host := p.Host
	for _, h := range r.Hosts {
		if h.Host != nil {
			candidate := strings.TrimRight(*h.Host, "\x00")
			if wechatnet.ValidHost(candidate) {
				host = candidate
				break
			}
		}
	}
	data := func(b *authwire.Buffer) []byte {
		if b == nil || !b.HasData {
			return nil
		}
		return append([]byte(nil), b.Data...)
	}
	return native.SessionFile{Profile: Profile, Experimental: true, ShortHost: host, ClientVersion: wire.Version, UIN: *r.UIN, SessionKey: key, Cookie: cookie, LoginSalt: public, MMTLSPSK: append([]byte(nil), p.MMTLS.PSK...), MMTLSTicket: append([]byte(nil), p.MMTLS.Ticket...), UserName: p.UserName, DeviceID: append([]byte(nil), p.DeviceID...), DeviceType: wire.DeviceType, ClientSessionKey: data(r.ClientSessionKey), ServerSessionKey: data(r.ServerSessionKey), AutoAuthKey: data(r.AutoAuthKey), DeviceProfilePath: p.DeviceProfilePath}, nil
}
