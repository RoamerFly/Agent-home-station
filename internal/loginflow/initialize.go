package loginflow

import (
	"context"
	"errors"
	"fmt"

	"wechat-bot/internal/business"
	"wechat-bot/internal/initwire"
	"wechat-bot/internal/mmtls"
	"wechat-bot/internal/native"
	"wechat-bot/internal/rqtx"
	"wechat-bot/internal/wechatnet"
)

type InitializationPage struct {
	Commands     int
	ContinueFlag *uint32
	Verified     bool
	// Private recovery material may contain contacts or message metadata.
	// The CLI stores it locally and prints only counts/continuation status.
	Response []byte
}

type InitializationOptions struct {
	Checkpoint         func(*native.SessionFile) error
	ResponseCheckpoint func([]byte) error
}

// InitializePage submits one newinit request and preserves its response. The
// recovered function does not branch on ContinueFlag, so this layer never loops
// based solely on its name. Unknown outcomes retain the in-flight marker.
func InitializePage(ctx context.Context, carrier Carrier, s *native.SessionFile, opt InitializationOptions) (InitializationPage, error) {
	var result InitializationPage
	if carrier == nil || s == nil || s.Profile != Profile || !s.Experimental || !wechatnet.ValidHost(s.ShortHost) || s.UserName == "" || len(s.DeviceID) != 16 || s.ClientVersion == 0 || s.DeviceType == "" {
		return result, errors.New("initialization requires a completed account session and matching device profile")
	}
	if s.InitializationInFlight {
		return result, errors.New("prior initialization outcome is unknown; inspect private recovery data before another request")
	}
	if s.InitializationVerified {
		return result, errors.New("initialization already verified")
	}
	businessSession := business.Session{ClientVersion: s.ClientVersion, UIN: s.UIN, SessionKey: s.SessionKey, Cookie: s.Cookie, LoginSalt: s.LoginSalt}
	if err := businessSession.Validate(); err != nil {
		return result, err
	}
	version, scene := int32(s.ClientVersion), uint32(3)
	lang := "zh_CN"
	buffer := func(data []byte) *initwire.Buffer {
		n := uint32(len(data))
		return &initwire.Buffer{Length: &n, Data: data, HasData: true}
	}
	payload, err := initwire.MarshalRequest(initwire.NewInitRequest{BaseRequest: &initwire.BaseRequest{SessionKey: s.SessionKey, HasSessionKey: true, UIN: &s.UIN, DeviceID: s.DeviceID, HasDeviceID: true, ClientVersion: &version, DeviceType: []byte(s.DeviceType), HasDeviceType: true, Scene: &scene}, UserName: &s.UserName, CurrentSynckey: buffer(s.CurrentSyncKey), MaxSynckey: buffer(s.MaxSyncKey), Language: &lang})
	if err != nil {
		return result, err
	}
	packet, err := business.EncodeOperation(payload, businessSession, business.InitCGI, rqtx.SignBody)
	if err != nil {
		return result, err
	}
	s.InitializationInFlight = true
	if opt.Checkpoint != nil {
		if err = opt.Checkpoint(s); err != nil {
			return result, errors.New("cannot persist initialization marker; request not sent")
		}
	}
	raw, err := carrier.Exchange(ctx, mmtls.Session{PSK: s.MMTLSPSK, Ticket: s.MMTLSTicket, Experimental: true}, "/cgi-bin/micromsg-bin/newinit", packet)
	if err != nil {
		return result, fmt.Errorf("initialization outcome unknown; not retried: %w", err)
	}
	plain, cookie, err := business.Decode(raw, businessSession)
	if err != nil {
		return result, fmt.Errorf("initialization response outcome unknown: %w", err)
	}
	result.Response = append([]byte(nil), plain...)
	if opt.ResponseCheckpoint != nil {
		if err = opt.ResponseCheckpoint(plain); err != nil {
			return result, errors.New("initialization response received but private checkpoint failed; not retried")
		}
	}
	r, err := initwire.ParseResponse(plain)
	if err != nil {
		return result, err
	}
	if err = r.ValidateForUse(); err != nil {
		return result, err
	}
	if r.CurrentSynckey == nil || !r.CurrentSynckey.HasData {
		return result, errors.New("initialization response lacks current sync state")
	}
	s.CurrentSyncKey = append([]byte(nil), r.CurrentSynckey.Data...)
	if r.MaxSynckey != nil && r.MaxSynckey.HasData {
		s.MaxSyncKey = append([]byte(nil), r.MaxSynckey.Data...)
	}
	if len(cookie) == 15 {
		s.Cookie = append([]byte(nil), cookie...)
	}
	s.InitializationPages++
	s.InitializationVerified = true
	s.InitializationInFlight = false
	if opt.Checkpoint != nil {
		if err = opt.Checkpoint(s); err != nil {
			return result, errors.New("initialization verified but updated state could not be saved; do not resubmit")
		}
	}
	result.Commands = len(r.CmdList)
	result.ContinueFlag = r.ContinueFlag
	result.Verified = s.InitializationVerified
	return result, nil
}
