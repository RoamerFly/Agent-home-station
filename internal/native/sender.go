// Package native connects the independently written protobuf, business envelope,
// RQTX interpreter and candidate MMTLS record layer. It never loads the original
// executable/shared library. A valid established account session is still needed.
package native

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/business"
	"wechat-bot/internal/mmtls"
	"wechat-bot/internal/rqtx"
	"wechat-bot/internal/sending"
	"wechat-bot/internal/wechatwire"
)

const profile = "wechat-ipad-20260913-experimental"
const textPath = "/cgi-bin/micromsg-bin/newsendmsg"

// SessionFile is our own explicit exchange format, not the original Redis format.
// Byte slices are Base64 JSON strings. Do not send this file to a chat or commit it.
// Experimental acknowledges that complete server interoperability is unverified.
type SessionFile struct {
	Profile                string `json:"profile"`
	Experimental           bool   `json:"enable_experimental_send"`
	ShortHost              string `json:"short_host"`
	ClientVersion          uint32 `json:"client_version"`
	UIN                    uint32 `json:"uin"`
	SessionKey             []byte `json:"session_key"`
	Cookie                 []byte `json:"cookie"`
	LoginSalt              []byte `json:"login_salt"`
	MMTLSPSK               []byte `json:"mmtls_psk"`
	MMTLSTicket            []byte `json:"mmtls_ticket"`
	UserName               string `json:"username,omitempty"`
	DeviceID               []byte `json:"device_id,omitempty"`
	DeviceType             string `json:"device_type,omitempty"`
	ClientSessionKey       []byte `json:"client_session_key,omitempty"`
	ServerSessionKey       []byte `json:"server_session_key,omitempty"`
	AutoAuthKey            []byte `json:"auto_auth_key,omitempty"`
	DeviceProfilePath      string `json:"device_profile_path,omitempty"`
	CurrentSyncKey         []byte `json:"current_sync_key,omitempty"`
	MaxSyncKey             []byte `json:"max_sync_key,omitempty"`
	InitializationVerified bool   `json:"initialization_verified,omitempty"`
	InitializationInFlight bool   `json:"initialization_in_flight,omitempty"`
	InitializationPages    uint32 `json:"initialization_pages,omitempty"`
}

type sender struct {
	session    SessionFile
	client     *http.Client
	gate       chan struct{}
	configured bool
}

// New loads a session without performing any network calls. An empty filename
// starts a daemon whose send endpoint returns transport_not_ready.
func New(filename string) (sending.Engine, error) {
	s := &sender{gate: make(chan struct{}, 1)}
	if filename == "" {
		return s, nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, errors.New("cannot open session file")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return nil, errors.New("invalid session file size")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&s.session); err != nil {
		return nil, errors.New("invalid session JSON")
	}
	var trailing any
	if err = d.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("session JSON must contain one object")
	}
	if s.session.Profile != profile || !s.session.Experimental {
		return nil, errors.New("session requires the explicit experimental protocol profile")
	}
	if !validHost(s.session.ShortHost) {
		return nil, errors.New("session short host is not a supported WeChat hostname")
	}
	if err = s.businessSession().Validate(); err != nil {
		return nil, err
	}
	// NewExchange validates opaque ticket framing and PSK length before readiness.
	if _, err = mmtls.NewExchange(s.mmtlsSession(), s.session.ShortHost, textPath, []byte{1}, time.Now(), rand.Reader); err != nil {
		return nil, errors.New("incomplete or unsupported MMTLS session")
	}
	s.client = &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second},
	}
	s.configured = true
	return s, nil
}

func validHost(h string) bool {
	if h != strings.ToLower(h) || len(h) > 253 || strings.ContainsAny(h, "/:@?#\\ \t\r\n") {
		return false
	}
	if !strings.HasSuffix(h, ".weixin.qq.com") && !strings.HasSuffix(h, ".wechat.com") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}

// Ready means configured for an experimental attempt. It is not a login-status
// probe and cannot establish that the remote server still accepts the session.
func (s *sender) Ready() bool { return s.configured }
func (s *sender) businessSession() business.Session {
	return business.Session{ClientVersion: s.session.ClientVersion, UIN: s.session.UIN, SessionKey: s.session.SessionKey, Cookie: s.session.Cookie, LoginSalt: s.session.LoginSalt}
}
func (s *sender) mmtlsSession() mmtls.Session {
	return mmtls.Session{PSK: s.session.MMTLSPSK, Ticket: s.session.MMTLSTicket}
}

func (s *sender) SendText(ctx context.Context, request sending.TextRequest) (sending.Receipt, error) {
	if !s.Ready() {
		return sending.Receipt{}, sending.ErrNotReady
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return sending.Receipt{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return sending.Receipt{}, err
	}
	now := time.Now()
	text, err := wechatwire.NewText(request.Recipient, request.Text, now)
	if err != nil {
		return sending.Receipt{}, err
	}
	payload, err := wechatwire.MarshalText(text)
	if err != nil {
		return sending.Receipt{}, err
	}
	packet, err := business.EncodeText(payload, s.businessSession(), rqtx.SignBody)
	if err != nil {
		return sending.Receipt{}, err
	}
	exchange, err := mmtls.NewExchange(s.mmtlsSession(), s.session.ShortHost, textPath, packet, now, rand.Reader)
	if err != nil {
		return sending.Receipt{}, err
	}
	url := "http://" + s.session.ShortHost + "/mmtls/" + strconv.FormatInt(now.Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(exchange.RequestBody()))
	if err != nil {
		return sending.Receipt{}, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "MicroMessenger Client")
	req.Close = true
	// From Do onward the server might have processed the request; never retry it.
	resp, err := s.client.Do(req)
	if err != nil {
		return sending.Receipt{}, sending.ErrOutcomeUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return sending.Receipt{}, sending.ErrOutcomeUnknown
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, business.MaxPayload+4097))
	if err != nil || len(raw) > business.MaxPayload+4096 {
		return sending.Receipt{}, sending.ErrOutcomeUnknown
	}
	wrapped, err := exchange.DecodeResponse(raw)
	if err != nil {
		return sending.Receipt{}, sending.ErrOutcomeUnknown
	}
	plain, cookie, err := business.Decode(wrapped, s.businessSession())
	if err != nil {
		return sending.Receipt{}, sending.ErrOutcomeUnknown
	}
	ack, err := wechatwire.ParseAcknowledgement(plain)
	if err != nil {
		return sending.Receipt{}, sending.ErrOutcomeUnknown
	}
	if ack.HasBaseResponse && ack.HasReturnCode && ack.ReturnCode != 0 {
		return sending.Receipt{}, sending.ErrRejected
	}
	if len(ack.Items) == 1 && ack.Items[0].HasReturnCode && ack.Items[0].ReturnCode != 0 {
		return sending.Receipt{}, sending.ErrRejected
	}
	serverID, err := ack.Validate(text)
	if err != nil {
		return sending.Receipt{}, sending.ErrOutcomeUnknown
	}
	if len(cookie) == 15 {
		s.session.Cookie = append([]byte(nil), cookie...)
	} // Memory only; no writes to the user's session file.
	return sending.Receipt{Recipient: text.Recipient, ClientMessageID: strconv.FormatUint(text.ClientMessageID, 10), ServerMessageID: serverID}, nil
}
