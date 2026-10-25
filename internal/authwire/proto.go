// Package authwire contains an offline, source-built subset of the recovered
// authorization protobuf. It does not open sockets or import the closed image.
package authwire

import (
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

const (
	maxPayload = 2 << 20
	maxFields  = 256
	maxDepth   = 12
)

var (
	ErrMalformed = errors.New("malformed authorization protobuf")
	ErrMissing   = errors.New("required authorization field is absent")
	ErrTooLarge  = errors.New("authorization protobuf exceeds local limits")
)

type field struct {
	n int
	w uint64
	v uint64
	b []byte
}

func fields(b []byte, d int, fn func(field) error) error {
	if d > maxDepth || len(b) > maxPayload {
		return ErrTooLarge
	}
	for count := 0; len(b) > 0; count++ {
		if count >= maxFields {
			return ErrTooLarge
		}
		tag, k := binary.Uvarint(b)
		if k <= 0 {
			return ErrMalformed
		}
		b = b[k:]
		f := field{n: int(tag >> 3), w: tag & 7}
		if f.n < 1 || f.n > 536870911 {
			return ErrMalformed
		}
		switch f.w {
		case 0:
			v, k := binary.Uvarint(b)
			if k <= 0 {
				return ErrMalformed
			}
			f.v = v
			b = b[k:]
		case 1:
			if len(b) < 8 {
				return ErrMalformed
			}
			b = b[8:]
		case 2:
			l, k := binary.Uvarint(b)
			if k <= 0 {
				return ErrMalformed
			}
			b = b[k:]
			if l > uint64(len(b)) || l > maxPayload {
				return ErrMalformed
			}
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
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}
func av(b []byte, n int, v uint64) []byte {
	b = binary.AppendUvarint(b, uint64(n<<3))
	return binary.AppendUvarint(b, v)
}
func ab(b []byte, n int, v []byte) []byte {
	b = binary.AppendUvarint(b, uint64(n<<3|2))
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}
func str(b []byte) (string, error) {
	if !utf8.Valid(b) {
		return "", ErrMalformed
	}
	return string(b), nil
}
func u32(v uint64) (uint32, error) {
	if v > 0xffffffff {
		return 0, ErrMalformed
	}
	return uint32(v), nil
}
func p32(v uint32) *uint32 { return &v }
func pi32(v int32) *int32  { return &v }
func ps(v string) *string  { return &v }
