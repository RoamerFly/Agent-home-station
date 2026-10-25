// Package initwire implements only the protobuf body of mm.NewInit.  It has no
// transport, encryption, cookie, or persistent-session responsibilities.
package initwire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"

	"wechat-bot/internal/loginwire"
)

const (
	MaxPayload  = 2 << 20
	maxFields   = 256
	maxDepth    = 12
	maxCommands = 128
	maxBlob     = 64 << 10
)

var (
	ErrMalformed    = errors.New("malformed NewInit protobuf")
	ErrMissingField = errors.New("required NewInit field is absent")
	ErrTooLarge     = errors.New("NewInit protobuf exceeds local limits")
	ErrRemote       = errors.New("NewInit response return code is non-zero")
)

// Optional scalar and bytes presence is represented explicitly. This matters
// because the recovered Go ABI represents optional scalars as pointers.
type BaseRequest = loginwire.BaseRequest
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

type NewInitRequest struct {
	BaseRequest    *BaseRequest
	UserName       *string
	CurrentSynckey *Buffer
	MaxSynckey     *Buffer
	Language       *string
}

// Command retains the raw CmdBuf.Buffer bytes. CmdBuf's declared iLen is kept
// as well so a caller can reject a protocol-inconsistent command deliberately.
type Command struct {
	ID     *int32
	CmdBuf *Buffer
	Raw    []byte
}
type NewInitResponse struct {
	BaseResponse   *BaseResponse
	CurrentSynckey *Buffer
	MaxSynckey     *Buffer
	ContinueFlag   *uint32
	SelectBitmap   *uint32
	CmdCount       *uint32
	CmdList        []Command
	Ratio          *uint32
}

func MarshalRequest(r NewInitRequest) ([]byte, error) {
	if r.BaseRequest == nil || r.UserName == nil || r.Language == nil {
		return nil, ErrMissingField
	}
	b, err := marshalBase(*r.BaseRequest)
	if err != nil {
		return nil, err
	}
	out := bytesField(nil, 1, b)
	if !utf8.ValidString(*r.UserName) || !utf8.ValidString(*r.Language) {
		return nil, ErrMalformed
	}
	out = bytesField(out, 2, []byte(*r.UserName))
	if r.CurrentSynckey != nil {
		out = bytesField(out, 3, marshalBuffer(*r.CurrentSynckey))
	}
	if r.MaxSynckey != nil {
		out = bytesField(out, 4, marshalBuffer(*r.MaxSynckey))
	}
	out = bytesField(out, 5, []byte(*r.Language))
	if len(out) > MaxPayload {
		return nil, ErrTooLarge
	}
	return out, nil
}

// BuildRequest is the compact construction API used by login flow code. It
// makes both sync-key wrappers present even when their byte strings are empty,
// matching the recovered pointer-backed SKBuiltinBuffer_t construction.
func BuildRequest(base loginwire.BaseRequest, user string, current, max []byte) ([]byte, error) {
	if len(current) > maxBlob || len(max) > maxBlob {
		return nil, ErrTooLarge
	}
	cn, mn := uint32(len(current)), uint32(len(max))
	lang := "zh_CN"
	return MarshalRequest(NewInitRequest{BaseRequest: &base, UserName: &user, CurrentSynckey: &Buffer{Length: &cn, Data: current, HasData: true}, MaxSynckey: &Buffer{Length: &mn, Data: max, HasData: true}, Language: &lang})
}

func ParseResponse(raw []byte) (NewInitResponse, error) {
	var out NewInitResponse
	if len(raw) == 0 || len(raw) > MaxPayload {
		return out, ErrMalformed
	}
	err := parse(raw, 0, func(f field) error {
		switch f.n {
		case 1:
			if f.w != 2 {
				return ErrMalformed
			}
			v, e := parseBaseResponse(f.b, 1)
			out.BaseResponse = v
			return e
		case 2:
			if f.w != 2 {
				return ErrMalformed
			}
			v, e := parseBuffer(f.b, 1)
			out.CurrentSynckey = v
			return e
		case 3:
			if f.w != 2 {
				return ErrMalformed
			}
			v, e := parseBuffer(f.b, 1)
			out.MaxSynckey = v
			return e
		case 4:
			if f.w != 0 {
				return ErrMalformed
			}
			v, e := u32(f.v)
			out.ContinueFlag = &v
			return e
		case 5:
			if f.w != 0 {
				return ErrMalformed
			}
			v, e := u32(f.v)
			out.SelectBitmap = &v
			return e
		case 6:
			if f.w != 0 {
				return ErrMalformed
			}
			v, e := u32(f.v)
			out.CmdCount = &v
			return e
		case 7:
			if f.w != 2 {
				return ErrMalformed
			}
			if len(out.CmdList) >= maxCommands {
				return ErrTooLarge
			}
			v, e := parseCommand(f.b, 1)
			if e != nil {
				return e
			}
			out.CmdList = append(out.CmdList, *v)
			return nil
		case 8:
			if f.w != 0 {
				return ErrMalformed
			}
			v, e := u32(f.v)
			out.Ratio = &v
			return e
		}
		return nil
	})
	if err == nil && out.CmdCount != nil && *out.CmdCount != uint32(len(out.CmdList)) {
		return out, fmt.Errorf("command count: %w", ErrMalformed)
	}
	return out, err
}

// ValidateNativeGate mirrors the post-unmarshal assumptions at Newinit
// 0xb9f220: CmdList must be non-empty and CurrentSynckey must be dereferenceable.
// It intentionally does not claim that native checked BaseResponse.ret there.
func (r NewInitResponse) ValidateNativeGate() error {
	if r.CurrentSynckey == nil || len(r.CmdList) == 0 {
		return ErrMissingField
	}
	return nil
}

// ValidateForUse is the safer application-level gate: absence is never
// interpreted as success, and an explicit server failure is returned to caller.
func (r NewInitResponse) ValidateForUse() error {
	if r.BaseResponse == nil || r.BaseResponse.Ret == nil {
		return ErrMissingField
	}
	if *r.BaseResponse.Ret != 0 {
		return fmt.Errorf("%w: %d", ErrRemote, *r.BaseResponse.Ret)
	}
	return r.ValidateNativeGate()
}

type field struct {
	n int
	w uint64
	v uint64
	b []byte
}

func parse(b []byte, depth int, visit func(field) error) error {
	if depth > maxDepth || len(b) > MaxPayload {
		return ErrTooLarge
	}
	for count := 0; len(b) > 0; count++ {
		if count >= maxFields {
			return ErrTooLarge
		}
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return ErrMalformed
		}
		b = b[n:]
		f := field{n: int(tag >> 3), w: tag & 7}
		if f.n < 1 {
			return ErrMalformed
		}
		switch f.w {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return ErrMalformed
			}
			f.v = v
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return ErrMalformed
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) || l > MaxPayload {
				return ErrMalformed
			}
			b = b[n:]
			f.b = b[:int(l)]
			b = b[int(l):]
		case 5:
			if len(b) < 4 {
				return ErrMalformed
			}
			b = b[4:]
		default:
			return ErrMalformed
		}
		if err := visit(f); err != nil {
			return err
		}
	}
	return nil
}
func varint(out []byte, n int, v uint64) []byte {
	out = binary.AppendUvarint(out, uint64(n<<3))
	return binary.AppendUvarint(out, v)
}
func bytesField(out []byte, n int, b []byte) []byte {
	out = binary.AppendUvarint(out, uint64(n<<3|2))
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}
func u32(v uint64) (uint32, error) {
	if v > 0xffffffff {
		return 0, ErrMalformed
	}
	return uint32(v), nil
}
func i32(v uint64) int32 { return int32(uint32(v)) }
func marshalBase(b BaseRequest) ([]byte, error) {
	if !b.HasSessionKey || len(b.SessionKey) > maxBlob || b.UIN == nil || !b.HasDeviceID || len(b.DeviceID) > maxBlob || b.ClientVersion == nil || !b.HasDeviceType || len(b.DeviceType) > maxBlob || b.Scene == nil {
		return nil, ErrMissingField
	}
	out := bytesField(nil, 1, b.SessionKey)
	out = varint(out, 2, uint64(*b.UIN))
	out = bytesField(out, 3, b.DeviceID)
	out = varint(out, 4, uint64(uint32(*b.ClientVersion)))
	out = bytesField(out, 5, b.DeviceType)
	return varint(out, 6, uint64(*b.Scene)), nil
}
func marshalBuffer(b Buffer) []byte {
	out := []byte(nil)
	if b.Length != nil {
		out = varint(out, 1, uint64(*b.Length))
	}
	if b.HasData {
		out = bytesField(out, 2, b.Data)
	}
	return out
}
func parseBuffer(raw []byte, d int) (*Buffer, error) {
	o := new(Buffer)
	err := parse(raw, d, func(f field) error {
		switch f.n {
		case 1:
			if f.w != 0 {
				return ErrMalformed
			}
			v, e := u32(f.v)
			o.Length = &v
			return e
		case 2:
			if f.w != 2 || len(f.b) > maxBlob {
				return ErrTooLarge
			}
			o.Data = append([]byte(nil), f.b...)
			o.HasData = true
		}
		return nil
	})
	if err == nil && o.Length != nil && o.HasData && *o.Length != uint32(len(o.Data)) {
		return nil, fmt.Errorf("buffer length: %w", ErrMalformed)
	}
	return o, err
}
func parseBaseResponse(raw []byte, d int) (*BaseResponse, error) {
	o := new(BaseResponse)
	err := parse(raw, d, func(f field) error {
		switch f.n {
		case 1:
			if f.w != 0 {
				return ErrMalformed
			}
			v := i32(f.v)
			o.Ret = &v
		case 2:
			if f.w != 2 || !utf8.Valid(f.b) {
				return ErrMalformed
			}
			s := string(f.b)
			o.Error = &String{Value: &s}
		}
		return nil
	})
	return o, err
}
func parseCommand(raw []byte, d int) (*Command, error) {
	o := new(Command)
	err := parse(raw, d, func(f field) error {
		switch f.n {
		case 1:
			if f.w != 0 {
				return ErrMalformed
			}
			v := i32(f.v)
			o.ID = &v
		case 2:
			if f.w != 2 {
				return ErrMalformed
			}
			v, e := parseBuffer(f.b, d+1)
			if e != nil {
				return e
			}
			o.CmdBuf = v
			if v.HasData {
				o.Raw = append([]byte(nil), v.Data...)
			}
		}
		return nil
	})
	return o, err
}
