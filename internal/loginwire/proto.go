// Package loginwire implements the small QR-login protobuf subset recovered
// from the pinned artifact's mm.proto descriptor.  It intentionally does not
// implement encryption, hybrid ECDH, MMTLS, or network I/O.
package loginwire

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
	ErrMalformed    = errors.New("malformed QR-login protobuf")
	ErrMissingField = errors.New("required QR-login field is absent")
	ErrTooLarge     = errors.New("QR-login protobuf exceeds local limits")
)

type field struct {
	number int
	wire   uint64
	value  uint64
	bytes  []byte
}

func parseFields(b []byte, depth int, visit func(field) error) error {
	if depth > maxDepth || len(b) > maxPayload {
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
		f := field{number: int(tag >> 3), wire: tag & 7}
		if f.number <= 0 || f.number > 536870911 {
			return ErrMalformed
		}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return ErrMalformed
			}
			f.value, b = v, b[n:]
		case 1:
			if len(b) < 8 {
				return ErrMalformed
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 {
				return ErrMalformed
			}
			b = b[n:]
			if l > uint64(len(b)) || l > maxPayload {
				return ErrMalformed
			}
			f.bytes, b = b[:int(l)], b[int(l):]
		case 5:
			if len(b) < 4 {
				return ErrMalformed
			}
			b = b[4:]
		default:
			return ErrMalformed // groups are deliberately unsupported.
		}
		if err := visit(f); err != nil {
			return err
		}
	}
	return nil
}

func appendVarint(dst []byte, field int, value uint64) []byte {
	dst = binary.AppendUvarint(dst, uint64(field<<3))
	return binary.AppendUvarint(dst, value)
}

func appendBytes(dst []byte, field int, value []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(field<<3|2))
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func checkString(b []byte) (string, error) {
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

func i32(v uint64) int32 { return int32(uint32(v)) }
