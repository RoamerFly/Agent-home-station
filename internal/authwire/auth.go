package authwire

import (
	"crypto/elliptic"
	"unicode/utf8"
)

type Buffer struct {
	Length  *uint32
	Data    []byte
	HasData bool
}
type BaseRequest struct {
	SessionKey    []byte
	HasSessionKey bool
	UIN           *uint32
	DeviceID      []byte
	HasDeviceID   bool
	ClientVersion *int32
	DeviceType    []byte
	HasDeviceType bool
	Scene         *uint32
}
type ECDHKey struct {
	NID *int32
	Key *Buffer
}
type BaseAuthReqInfo struct {
	WTLoginRequest      *Buffer
	ClientDBEncryptKey  *Buffer
	ClientDBEncryptInfo *Buffer
	AuthRequestFlag     *uint32
	AuthTicket          *string
}

// Secret deliberately redacts its content for ordinary fmt logging.
type Secret []byte

func (Secret) String() string { return "[redacted]" }

type DeviceFields struct {
	IMEI, SoftType, ClientSeqID, Signature, DeviceName, DeviceType, Language, TimeZone *string
	Channel                                                                            *int32
	Timestamp                                                                          *uint32
	DeviceBrand, DeviceModel, OSType, RealCountry, BundleID, AdSource, IPhoneVersion   *string
	BuiltinIPSeq, InputType                                                            *uint32
	ClientCheckData, ExtSpamInfo                                                       *Buffer // caller supplies actual proof or leaves absent.
}
type RequestInput struct {
	Base             BaseRequest
	BaseAuth         *BaseAuthReqInfo
	RandomKey        []byte
	ClientECDHPublic []byte
	UserName         string
	Password         Secret
	Device           DeviceFields
}

// BuildRequest encodes mm.SecManualLoginRequest. It encodes only descriptor
// fields supplied by the caller; it never fabricates extSpamInfo or device proof.
func BuildRequest(in RequestInput) ([]byte, error) {
	if len(in.RandomKey) == 0 || len(in.RandomKey) > 4096 || len(in.ClientECDHPublic) != 57 || in.UserName == "" || len(in.Password) == 0 || !utf8.ValidString(in.UserName) || !utf8.Valid(in.Password) {
		return nil, ErrMissing
	}
	if !validPublic(in.ClientECDHPublic) {
		return nil, ErrInvalidKey
	}
	rsa, err := marshalRSA(in)
	if err != nil {
		return nil, err
	}
	aesPart, err := marshalAES(in)
	if err != nil {
		return nil, err
	}
	out := ab(nil, 1, rsa)
	return ab(out, 2, aesPart), nil
}

func validPublic(b []byte) bool {
	if len(b) != 57 {
		return false
	}
	c := elliptic.P224()
	x, y := elliptic.Unmarshal(c, b)
	return x != nil && y != nil && c.IsOnCurve(x, y)
}

func marshalRSA(in RequestInput) ([]byte, error) {
	out := ab(nil, 1, marshalBuffer(Buffer{Length: p32(uint32(len(in.RandomKey))), Data: in.RandomKey, HasData: true}))
	nid := int32(713)
	pk := ECDHKey{NID: &nid, Key: &Buffer{Length: p32(uint32(len(in.ClientECDHPublic))), Data: in.ClientECDHPublic, HasData: true}}
	out = ab(out, 2, marshalECDH(pk))
	out = ab(out, 3, []byte(in.UserName))
	out = ab(out, 4, []byte(in.Password))
	return ab(out, 5, []byte(in.Password)), nil
}
func marshalAES(in RequestInput) ([]byte, error) {
	base, err := marshalBase(in.Base)
	if err != nil {
		return nil, err
	}
	out := ab(nil, 1, base)
	if in.BaseAuth != nil {
		out = ab(out, 2, marshalBaseAuth(*in.BaseAuth))
	}
	d := in.Device
	var e error
	for _, x := range []struct {
		n int
		s *string
	}{{3, d.IMEI}, {4, d.SoftType}, {6, d.ClientSeqID}, {7, d.Signature}, {8, d.DeviceName}, {9, d.DeviceType}, {10, d.Language}, {11, d.TimeZone}, {15, d.DeviceBrand}, {16, d.DeviceModel}, {17, d.OSType}, {18, d.RealCountry}, {19, d.BundleID}, {20, d.AdSource}, {21, d.IPhoneVersion}} {
		if out, e = addStr(out, x.n, x.s); e != nil {
			return nil, e
		}
	}
	if d.BuiltinIPSeq != nil {
		out = av(out, 5, uint64(*d.BuiltinIPSeq))
	}
	if d.Channel != nil {
		out = av(out, 13, uint64(uint32(*d.Channel)))
	}
	if d.Timestamp != nil {
		out = av(out, 14, uint64(*d.Timestamp))
	}
	if d.InputType != nil {
		out = av(out, 22, uint64(*d.InputType))
	}
	if d.ClientCheckData != nil {
		out = ab(out, 23, marshalBuffer(*d.ClientCheckData))
	}
	if d.ExtSpamInfo != nil {
		out = ab(out, 24, marshalBuffer(*d.ExtSpamInfo))
	}
	return out, nil
}
func addStr(out []byte, n int, s *string) ([]byte, error) {
	if s == nil {
		return out, nil
	}
	if !utf8.ValidString(*s) || len(*s) > maxPayload {
		return nil, ErrMalformed
	}
	return ab(out, n, []byte(*s)), nil
}
func marshalBase(v BaseRequest) ([]byte, error) {
	var o []byte
	if v.HasSessionKey {
		o = ab(o, 1, v.SessionKey)
	}
	if v.UIN != nil {
		o = av(o, 2, uint64(*v.UIN))
	}
	if v.HasDeviceID {
		o = ab(o, 3, v.DeviceID)
	}
	if v.ClientVersion != nil {
		o = av(o, 4, uint64(uint32(*v.ClientVersion)))
	}
	if v.HasDeviceType {
		o = ab(o, 5, v.DeviceType)
	}
	if v.Scene != nil {
		o = av(o, 6, uint64(*v.Scene))
	}
	return o, nil
}
func marshalBuffer(v Buffer) []byte {
	var o []byte
	if v.Length != nil {
		o = av(o, 1, uint64(*v.Length))
	}
	if v.HasData {
		o = ab(o, 2, v.Data)
	}
	return o
}
func marshalECDH(v ECDHKey) []byte {
	var o []byte
	if v.NID != nil {
		o = av(o, 1, uint64(uint32(*v.NID)))
	}
	if v.Key != nil {
		o = ab(o, 2, marshalBuffer(*v.Key))
	}
	return o
}
func marshalBaseAuth(v BaseAuthReqInfo) []byte {
	var o []byte
	if v.WTLoginRequest != nil {
		o = ab(o, 1, marshalBuffer(*v.WTLoginRequest))
	}
	if v.ClientDBEncryptKey != nil {
		o = ab(o, 4, marshalBuffer(*v.ClientDBEncryptKey))
	}
	if v.ClientDBEncryptInfo != nil {
		o = ab(o, 5, marshalBuffer(*v.ClientDBEncryptInfo))
	}
	if v.AuthRequestFlag != nil {
		o = av(o, 6, uint64(*v.AuthRequestFlag))
	}
	if v.AuthTicket != nil {
		o, _ = addStr(o, 7, v.AuthTicket)
	}
	return o
}

type BaseResponse struct {
	Ret   *int32
	Error *string
}
type Host struct{ Host, Redirect *string }
type AuthorizationResponse struct {
	Base                                                                 *BaseResponse
	SectionFlag                                                          *uint32
	UIN                                                                  *uint32
	ServerECDH                                                           *ECDHKey
	EncryptedSessionKey, AutoAuthKey, ClientSessionKey, ServerSessionKey *Buffer
	Account                                                              *Account
	Hosts                                                                []Host
}
type Account struct {
	UserName, NickName, Alias *string
	Status                    *uint32
}

func (r AuthorizationResponse) Code() (int32, error) {
	if r.Base == nil || r.Base.Ret == nil {
		return 0, ErrMissing
	}
	return *r.Base.Ret, nil
}
func ParseResponse(raw []byte) (AuthorizationResponse, error) {
	var o AuthorizationResponse
	if len(raw) == 0 || len(raw) > maxPayload {
		return o, ErrMalformed
	}
	err := fields(raw, 0, func(f field) error {
		switch f.n {
		case 1:
			if f.w != 2 {
				return ErrMalformed
			}
			v, e := parseBaseResp(f.b)
			o.Base = v
			return e
		case 2:
			if f.w != 0 {
				return ErrMalformed
			}
			v, e := u32(f.v)
			if e == nil {
				o.SectionFlag = &v
			}
			return e
		case 3:
			if f.w != 2 {
				return ErrMalformed
			}
			return parseAuth(f.b, &o)
		case 4:
			if f.w != 2 {
				return ErrMalformed
			}
			v, e := parseAccount(f.b)
			o.Account = v
			return e
		case 5:
			if f.w != 2 {
				return ErrMalformed
			}
			return parseNetwork(f.b, &o)
		}
		return nil
	})
	return o, err
}
func parseBaseResp(b []byte) (*BaseResponse, error) {
	o := new(BaseResponse)
	e := fields(b, 1, func(f field) error {
		if f.n == 1 {
			if f.w != 0 {
				return ErrMalformed
			}
			v := int32(uint32(f.v))
			o.Ret = &v
		}
		if f.n == 2 {
			if f.w != 2 {
				return ErrMalformed
			}
			return fields(f.b, 2, func(g field) error {
				if g.n == 1 {
					if g.w != 2 {
						return ErrMalformed
					}
					s, e := str(g.b)
					if e == nil {
						o.Error = &s
					}
					return e
				}
				return nil
			})
		}
		return nil
	})
	return o, e
}
func parseAuth(b []byte, o *AuthorizationResponse) error {
	return fields(b, 1, func(f field) error {
		switch f.n {
		case 1:
			if f.w != 0 {
				return ErrMalformed
			}
			v, e := u32(f.v)
			if e == nil {
				o.UIN = &v
			}
			return e
		case 2:
			if f.w != 2 {
				return ErrMalformed
			}
			v, e := parseECDH(f.b)
			o.ServerECDH = v
			return e
		case 3, 4, 23, 24:
			if f.w != 2 {
				return ErrMalformed
			}
			v, e := parseBuffer(f.b, 2)
			if f.n == 3 {
				o.EncryptedSessionKey = v
			}
			if f.n == 4 {
				o.AutoAuthKey = v
			}
			if f.n == 23 {
				o.ClientSessionKey = v
			}
			if f.n == 24 {
				o.ServerSessionKey = v
			}
			return e
		}
		return nil
	})
}
func parseECDH(b []byte) (*ECDHKey, error) {
	o := new(ECDHKey)
	e := fields(b, 2, func(f field) error {
		if f.n == 1 {
			if f.w != 0 {
				return ErrMalformed
			}
			v := int32(uint32(f.v))
			o.NID = &v
		}
		if f.n == 2 {
			if f.w != 2 {
				return ErrMalformed
			}
			v, e := parseBuffer(f.b, 3)
			o.Key = v
			return e
		}
		return nil
	})
	return o, e
}
func parseBuffer(b []byte, d int) (*Buffer, error) {
	o := new(Buffer)
	e := fields(b, d, func(f field) error {
		if f.n == 1 {
			if f.w != 0 {
				return ErrMalformed
			}
			v, e := u32(f.v)
			if e == nil {
				o.Length = &v
			}
			return e
		}
		if f.n == 2 {
			if f.w != 2 {
				return ErrMalformed
			}
			o.Data = append([]byte(nil), f.b...)
			o.HasData = true
		}
		return nil
	})
	if e == nil && o.Length != nil && o.HasData && uint64(*o.Length) != uint64(len(o.Data)) {
		return nil, ErrMalformed
	}
	return o, e
}
func parseAccount(b []byte) (*Account, error) {
	o := new(Account)
	e := fields(b, 1, func(f field) error {
		if f.n == 8 {
			if f.w != 0 {
				return ErrMalformed
			}
			v, e := u32(f.v)
			if e == nil {
				o.Status = &v
			}
			return e
		}
		if f.n == 1 || f.n == 2 || f.n == 6 {
			if f.w != 2 {
				return ErrMalformed
			}
			s, e := str(f.b)
			if e != nil {
				return e
			}
			if f.n == 1 {
				o.UserName = &s
			}
			if f.n == 2 {
				o.NickName = &s
			}
			if f.n == 6 {
				o.Alias = &s
			}
			return nil
		}
		return nil
	})
	return o, e
}
func parseNetwork(b []byte, o *AuthorizationResponse) error {
	return fields(b, 1, func(f field) error {
		if f.n != 1 {
			return nil
		}
		if f.w != 2 {
			return ErrMalformed
		}
		return fields(f.b, 2, func(g field) error {
			if g.n != 2 {
				return nil
			}
			if g.w != 2 {
				return ErrMalformed
			}
			h := Host{}
			e := fields(g.b, 3, func(x field) error {
				if x.n == 1 || x.n == 2 {
					if x.w != 2 {
						return ErrMalformed
					}
					s, e := str(x.b)
					if e != nil {
						return e
					}
					if x.n == 1 {
						h.Host = &s
					} else {
						h.Redirect = &s
					}
				}
				return nil
			})
			if e == nil {
				o.Hosts = append(o.Hosts, h)
			}
			return e
		})
	})
}
