package loginwire

import (
	"fmt"
	"unicode/utf8"
)

// Optional scalars are pointers so a decoded absent zero is never confused
// with a decoded explicit zero.  Bytes use the accompanying Has* flag for the
// same reason.
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
type Buffer struct {
	Length  *uint32
	Data    []byte
	HasData bool
}
type String struct{ Value *string }
type BaseResponse struct {
	Ret   *int32
	Error *String
}

// Names retained from the recovered descriptor for callers that need to map
// this package directly to mm.proto terms.
type SKBuiltinBuffer = Buffer
type SKBuiltinString = String

type QRRequestOptions struct {
	Opcode                  *uint32
	DeviceName              *string
	UserName                *string
	ExtDevLoginType         *uint32
	HardwareExtra           *string
	SoftType                *string
	MessageContextPublicKey *Buffer
}

// BuildQRRequest creates mm.GetLoginQRCodeRequest.  The recovered descriptor
// contains no DeviceToken field: a device-token proof belongs to the separate
// fpinitnl/transport layer and is deliberately not silently inserted here.
func BuildQRRequest(base BaseRequest, randomKey []byte, opt QRRequestOptions) ([]byte, error) {
	if len(randomKey) == 0 || len(randomKey) > 4096 {
		return nil, fmt.Errorf("random key: %w", ErrMissingField)
	}
	b, err := marshalBaseRequest(base)
	if err != nil {
		return nil, err
	}
	out := appendBytes(nil, 1, b)
	out = appendBytes(out, 2, marshalBuffer(Buffer{Length: ptrU32(uint32(len(randomKey))), Data: randomKey, HasData: true}))
	if opt.Opcode != nil {
		out = appendVarint(out, 3, uint64(*opt.Opcode))
	}
	var e error
	if out, e = appendOptionalString(out, 4, opt.DeviceName); e != nil {
		return nil, e
	}
	if out, e = appendOptionalString(out, 5, opt.UserName); e != nil {
		return nil, e
	}
	if opt.ExtDevLoginType != nil {
		out = appendVarint(out, 6, uint64(*opt.ExtDevLoginType))
	}
	if out, e = appendOptionalString(out, 7, opt.HardwareExtra); e != nil {
		return nil, e
	}
	if out, e = appendOptionalString(out, 8, opt.SoftType); e != nil {
		return nil, e
	}
	if opt.MessageContextPublicKey != nil {
		out = appendBytes(out, 9, marshalBuffer(*opt.MessageContextPublicKey))
	}
	return out, nil
}

type PollRequestOptions struct{ Opcode *uint32 }

// BuildPollRequest creates mm.CheckLoginQRCodeRequest. timestamp is supplied
// by the caller so retries and tests cannot accidentally use an implicit clock.
func BuildPollRequest(base BaseRequest, randomKey []byte, uuid string, timestamp uint32, opt PollRequestOptions) ([]byte, error) {
	if len(randomKey) == 0 || len(randomKey) > 4096 || uuid == "" || !utf8.ValidString(uuid) {
		return nil, ErrMissingField
	}
	b, err := marshalBaseRequest(base)
	if err != nil {
		return nil, err
	}
	out := appendBytes(nil, 1, b)
	out = appendBytes(out, 2, marshalBuffer(Buffer{Length: ptrU32(uint32(len(randomKey))), Data: randomKey, HasData: true}))
	out = appendBytes(out, 3, []byte(uuid))
	out = appendVarint(out, 4, uint64(timestamp))
	if opt.Opcode != nil {
		out = appendVarint(out, 5, uint64(*opt.Opcode))
	}
	return out, nil
}

type QRResponse struct {
	Base        *BaseResponse
	QRCode      *Buffer
	UUID        *string
	CheckTime   *uint32
	NotifyKey   *Buffer
	ExpiredTime *uint32
}

// Code returns an error for a missing base response or return code.  It never
// maps absence to a successful zero response.
func (r QRResponse) Code() (int32, error) { return responseCode(r.Base) }

func ParseQRResponse(raw []byte) (QRResponse, error) {
	var out QRResponse
	if len(raw) == 0 || len(raw) > maxPayload {
		return out, ErrMalformed
	}
	err := parseFields(raw, 0, func(f field) error {
		switch f.number {
		case 1:
			if f.wire != 2 {
				return ErrMalformed
			}
			v, e := parseBaseResponse(f.bytes)
			out.Base = v
			return e
		case 2:
			if f.wire != 2 {
				return ErrMalformed
			}
			v, e := parseBuffer(f.bytes, 1)
			out.QRCode = v
			return e
		case 3:
			if f.wire != 2 {
				return ErrMalformed
			}
			s, e := checkString(f.bytes)
			if e == nil {
				out.UUID = &s
			}
			return e
		case 4:
			if f.wire != 0 {
				return ErrMalformed
			}
			v, e := u32(f.value)
			if e == nil {
				out.CheckTime = &v
			}
			return e
		case 5:
			if f.wire != 2 {
				return ErrMalformed
			}
			v, e := parseBuffer(f.bytes, 1)
			out.NotifyKey = v
			return e
		case 6:
			if f.wire != 0 {
				return ErrMalformed
			}
			v, e := u32(f.value)
			if e == nil {
				out.ExpiredTime = &v
			}
			return e
		}
		return nil
	})
	return out, err
}

type PollResponse struct {
	Base          *BaseResponse
	NotifyPackage *NotifyPackage
}

func (r PollResponse) Code() (int32, error) { return responseCode(r.Base) }
func ParsePollResponse(raw []byte) (PollResponse, error) {
	var out PollResponse
	if len(raw) == 0 || len(raw) > maxPayload {
		return out, ErrMalformed
	}
	err := parseFields(raw, 0, func(f field) error {
		switch f.number {
		case 1:
			if f.wire != 2 {
				return ErrMalformed
			}
			v, e := parseBaseResponse(f.bytes)
			out.Base = v
			return e
		case 3:
			if f.wire != 2 {
				return ErrMalformed
			}
			v, e := parseNotifyPackage(f.bytes)
			out.NotifyPackage = v
			return e
		}
		return nil
	})
	return out, err
}

// NotifyPackage is the outer, encrypted container.  NotifyData is not decoded
// here; callers decrypt it with the prior QR response's notify key then pass
// the plaintext to ParseNotify.
type NotifyPackage struct {
	NotifyData *Buffer
	Opcode     *uint32
}

type LoginQRCodeNotifyPkg = NotifyPackage

func parseNotifyPackage(raw []byte) (*NotifyPackage, error) {
	out := new(NotifyPackage)
	err := parseFields(raw, 1, func(f field) error {
		switch f.number {
		case 1:
			if f.wire != 2 {
				return ErrMalformed
			}
			v, e := parseBuffer(f.bytes, 2)
			out.NotifyData = v
			return e
		case 2:
			if f.wire != 0 {
				return ErrMalformed
			}
			v, e := u32(f.value)
			if e == nil {
				out.Opcode = &v
			}
			return e
		}
		return nil
	})
	return out, err
}

// Notify is the decrypted mm.LoginQRCodeNotify payload. Password is retained
// only for the next authorization step and has no String method to reduce
// accidental logging.
type Notify struct {
	UUID                    *string
	Status                  *uint32
	UserName                *string
	Password                *string
	HeadImageURL            *string
	PushLoginURLExpiredTime *uint32
	NickName                *string
	ExpiredTime             *uint32
}

type LoginQRCodeNotify = Notify

func ParseNotify(raw []byte) (Notify, error) {
	var out Notify
	if len(raw) == 0 || len(raw) > maxPayload {
		return out, ErrMalformed
	}
	err := parseFields(raw, 0, func(f field) error {
		if f.number == 2 || f.number == 6 || f.number == 8 {
			if f.wire != 0 {
				return ErrMalformed
			}
			v, e := u32(f.value)
			if e != nil {
				return e
			}
			if f.number == 2 {
				out.Status = &v
			}
			if f.number == 6 {
				out.PushLoginURLExpiredTime = &v
			}
			if f.number == 8 {
				out.ExpiredTime = &v
			}
			return nil
		}
		if f.number >= 1 && f.number <= 5 || f.number == 7 {
			if f.wire != 2 {
				return ErrMalformed
			}
			s, e := checkString(f.bytes)
			if e != nil {
				return e
			}
			switch f.number {
			case 1:
				out.UUID = &s
			case 3:
				out.UserName = &s
			case 4:
				out.Password = &s
			case 5:
				out.HeadImageURL = &s
			case 7:
				out.NickName = &s
			}
		}
		return nil
	})
	return out, err
}

func marshalBaseRequest(v BaseRequest) ([]byte, error) {
	var out []byte
	if v.HasSessionKey {
		out = appendBytes(out, 1, v.SessionKey)
	}
	if v.UIN != nil {
		out = appendVarint(out, 2, uint64(*v.UIN))
	}
	if v.HasDeviceID {
		out = appendBytes(out, 3, v.DeviceID)
	}
	if v.ClientVersion != nil {
		out = appendVarint(out, 4, uint64(uint32(*v.ClientVersion)))
	}
	if v.HasDeviceType {
		out = appendBytes(out, 5, v.DeviceType)
	}
	if v.Scene != nil {
		out = appendVarint(out, 6, uint64(*v.Scene))
	}
	if len(out) > maxPayload {
		return nil, ErrTooLarge
	}
	return out, nil
}
func marshalBuffer(v Buffer) []byte {
	var out []byte
	if v.Length != nil {
		out = appendVarint(out, 1, uint64(*v.Length))
	}
	if v.HasData {
		out = appendBytes(out, 2, v.Data)
	}
	return out
}
func appendOptionalString(out []byte, n int, v *string) ([]byte, error) {
	if v == nil {
		return out, nil
	}
	if !utf8.ValidString(*v) || len(*v) > maxPayload {
		return nil, ErrMalformed
	}
	return appendBytes(out, n, []byte(*v)), nil
}
func parseBuffer(raw []byte, depth int) (*Buffer, error) {
	out := new(Buffer)
	err := parseFields(raw, depth, func(f field) error {
		switch f.number {
		case 1:
			if f.wire != 0 {
				return ErrMalformed
			}
			v, e := u32(f.value)
			if e == nil {
				out.Length = &v
			}
			return e
		case 2:
			if f.wire != 2 {
				return ErrMalformed
			}
			out.Data = append([]byte(nil), f.bytes...)
			out.HasData = true
		}
		return nil
	})
	if err == nil && out.Length != nil && out.HasData && uint64(*out.Length) != uint64(len(out.Data)) {
		return nil, ErrMalformed
	}
	return out, err
}
func parseBaseResponse(raw []byte) (*BaseResponse, error) {
	out := new(BaseResponse)
	err := parseFields(raw, 1, func(f field) error {
		switch f.number {
		case 1:
			if f.wire != 0 {
				return ErrMalformed
			}
			v := i32(f.value)
			out.Ret = &v
			return nil
		case 2:
			if f.wire != 2 {
				return ErrMalformed
			}
			s, e := parseString(rawOr(f.bytes))
			out.Error = s
			return e
		}
		return nil
	})
	return out, err
}
func parseString(raw []byte) (*String, error) {
	out := new(String)
	err := parseFields(raw, 2, func(f field) error {
		if f.number == 1 {
			if f.wire != 2 {
				return ErrMalformed
			}
			s, e := checkString(f.bytes)
			if e == nil {
				out.Value = &s
			}
			return e
		}
		return nil
	})
	return out, err
}
func rawOr(b []byte) []byte { return b }
func responseCode(b *BaseResponse) (int32, error) {
	if b == nil || b.Ret == nil {
		return 0, ErrMissingField
	}
	return *b.Ret, nil
}
func ptrU32(v uint32) *uint32 { return &v }
