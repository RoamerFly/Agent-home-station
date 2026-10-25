package loginwire

import "unicode/utf8"

// DeviceAttribute preserves the ordered TrustDeviceInfo records. Values come
// from the caller's persistent device profile, not from this encoder.
type DeviceAttribute struct{ Key, Value string }

// BuildTrustRequest encodes TrustReq/TrustData. SI is tag 2 in the current
// ELF's TrustData Go type; that field is absent from its older mm descriptor.
// This version difference is recorded in device-token-go-layouts.json.
func BuildTrustRequest(attributes []DeviceAttribute, si, md *string) ([]byte, error) {
	if len(attributes) == 0 || len(attributes) > 128 {
		return nil, ErrMissingField
	}
	var data []byte
	seen := make(map[string]bool, len(attributes))
	for _, attribute := range attributes {
		if attribute.Key == "" || len(attribute.Key) > 128 || len(attribute.Value) > 65536 || !utf8.ValidString(attribute.Key) || !utf8.ValidString(attribute.Value) || seen[attribute.Key] {
			return nil, ErrMalformed
		}
		seen[attribute.Key] = true
		item := appendBytes(nil, 1, []byte(attribute.Key))
		item = appendBytes(item, 2, []byte(attribute.Value))
		data = appendBytes(data, 1, item)
		if len(data) > maxPayload/2 {
			return nil, ErrTooLarge
		}
	}
	var err error
	data, err = appendOptionalString(data, 2, si)
	if err != nil {
		return nil, err
	}
	out := appendBytes(nil, 1, data)
	out, err = appendOptionalString(out, 2, md)
	if err != nil {
		return nil, err
	}
	if len(out) > maxPayload {
		return nil, ErrTooLarge
	}
	return out, nil
}

type ZTData struct {
	Version                   *string
	Encrypted                 *uint32
	Data                      []byte
	Timestamp, Operation, UIN *uint32
}

// BuildFingerprintRequest wraps already encrypted trust data in FPFresh.
// Encryption and network I/O are intentionally separate from the wire codec.
func BuildFingerprintRequest(base BaseRequest, sessionKey []byte, data ZTData) ([]byte, error) {
	if len(sessionKey) != 16 || len(data.Data) == 0 || len(data.Data) > maxPayload/2 {
		return nil, ErrMissingField
	}
	b, err := marshalBaseRequest(base)
	if err != nil {
		return nil, err
	}
	z, err := appendOptionalString(nil, 1, data.Version)
	if err != nil {
		return nil, err
	}
	if data.Encrypted != nil {
		z = appendVarint(z, 2, uint64(*data.Encrypted))
	}
	z = appendBytes(z, 3, data.Data)
	for _, f := range []struct {
		n int
		v *uint32
	}{{4, data.Timestamp}, {5, data.Operation}, {6, data.UIN}} {
		if f.v != nil {
			z = appendVarint(z, f.n, uint64(*f.v))
		}
	}
	out := appendBytes(nil, 1, b)
	out = appendBytes(out, 2, sessionKey)
	return appendBytes(out, 3, z), nil
}

// SecretText is persisted only by the calling flow. fmt logging redacts it.
type SecretText string

func (SecretText) String() string { return "[redacted]" }

type TrustResponse struct {
	Base           *BaseResponse
	HasData        bool
	Token          *SecretText
	Timestamp      *uint32
	SoftData       []byte
	SoftConfig     *SecretText
	SoftPayload    []byte
	HasSoftPayload bool
}

func (r TrustResponse) Code() (int32, error) { return responseCode(r.Base) }

func ParseTrustResponse(raw []byte) (TrustResponse, error) {
	var out TrustResponse
	if len(raw) == 0 || len(raw) > maxPayload {
		return out, ErrMalformed
	}
	err := parseFields(raw, 0, func(f field) error {
		switch f.number {
		case 1:
			if f.wire != 2 {
				return ErrMalformed
			}
			v, err := parseBaseResponse(f.bytes)
			out.Base = v
			return err
		case 2:
			if f.wire != 2 {
				return ErrMalformed
			}
			out.HasData = true
			return parseFields(f.bytes, 1, func(g field) error {
				switch g.number {
				case 2:
					if g.wire != 2 {
						return ErrMalformed
					}
					out.SoftData = append([]byte(nil), g.bytes...)
					return parseFields(g.bytes, 2, func(h field) error {
						switch h.number {
						case 1:
							if h.wire != 2 {
								return ErrMalformed
							}
							s, err := checkString(h.bytes)
							if err != nil {
								return err
							}
							secret := SecretText(s)
							out.SoftConfig = &secret
						case 2:
							if h.wire != 2 {
								return ErrMalformed
							}
							out.HasSoftPayload = true
							out.SoftPayload = append([]byte(nil), h.bytes...)
						}
						return nil
					})
				case 3:
					if g.wire != 2 {
						return ErrMalformed
					}
					s, err := checkString(g.bytes)
					if err != nil {
						return err
					}
					value := SecretText(s)
					out.Token = &value
				case 4:
					if g.wire != 0 {
						return ErrMalformed
					}
					v, err := u32(g.value)
					if err != nil {
						return err
					}
					out.Timestamp = &v
				}
				return nil
			})
		}
		return nil
	})
	return out, err
}
