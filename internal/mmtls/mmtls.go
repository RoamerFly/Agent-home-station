// Package mmtls builds and verifies the short-connection record layout inferred
// from the pinned sample. It is deliberately transport-only: it neither dials a
// server nor knows how a WeChat session, PSK, or ticket was obtained.
//
// Record constants and key labels were cross-checked against the pinned binary's
// static decompilation and an unlicensed historical implementation. They remain
// candidates until compared with a controlled, authorized live-session capture.
package mmtls

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	recordHeaderSize = 5
	tagSize          = 16
	maxRecordPayload = 65535
	maxTicketSize    = 60000

	recordNoop        = 0x19
	recordBusiness    = 0x17
	recordClose       = 0x15
	recordServerHello = 0x16
	versionHi         = 0xf1
	versionLo         = 0x03
)

var (
	ErrMalformed = errors.New("mmtls: malformed record stream")
	ErrAuth      = errors.New("mmtls: record authentication failed")
)

// Session is the already-established short-connection material. PSK is a
// 32-byte HKDF PRK candidate. Ticket is an opaque early-data part whose first
// four bytes are its big-endian payload length. This format is inferred from
// the sample's parser; callers must not substitute a login ticket string.
type Session struct {
	PSK    []byte
	Ticket []byte
	// Experimental is true for material produced by Handshake.Complete because
	// the supplied static evidence does not recover a pinned long-term server
	// signature verification key. It must not be interpreted as authenticated.
	Experimental bool
}

// Exchange owns its keys and sequence numbers. It is single-use and has no
// package global state, so independent requests cannot accidentally reuse a
// nonce through shared counters.
type Exchange struct {
	request          []byte
	clientTranscript []byte
	psk              [32]byte
}

// NewExchange prepares a candidate MMTLS short POST request. entropy supplies
// exactly 32 random bytes for the client hello. The returned bytes must only be
// sent after an authorized integration has independently validated this layout.
func NewExchange(session Session, host, path string, business []byte, now time.Time, entropy io.Reader) (*Exchange, error) {
	if err := validateSession(session); err != nil {
		return nil, err
	}
	if err := validateHostPath(host, path); err != nil {
		return nil, err
	}
	if entropy == nil {
		return nil, errors.New("mmtls: entropy is required")
	}
	if len(business) > maxRecordPayload-tagSize {
		return nil, errors.New("mmtls: business payload exceeds record limit")
	}

	requestData, err := makeRequestData(host, path, business)
	if err != nil {
		return nil, err
	}
	var random [32]byte
	if _, err := io.ReadFull(entropy, random[:]); err != nil {
		return nil, fmt.Errorf("mmtls: read entropy: %w", err)
	}
	clientHello, err := makeClientHello(session.Ticket, random[:], now)
	if err != nil {
		return nil, err
	}
	clientTranscript := append([]byte(nil), clientHello...)
	earlyKey := hkdfExpand(session.PSK, append([]byte("early data key expansion"), sha256Sum(clientHello)...), 28)
	key, iv := earlyKey[:16], earlyKey[16:]

	noop := makeNoop(now)
	request := appendRecord(nil, recordNoop, clientHello)
	var errSeal error
	request, errSeal = appendEncrypted(request, key, iv, 1, recordNoop, noop)
	if errSeal != nil {
		return nil, errSeal
	}
	request, errSeal = appendEncrypted(request, key, iv, 2, recordBusiness, requestData)
	if errSeal != nil {
		return nil, errSeal
	}
	request, errSeal = appendEncrypted(request, key, iv, 3, recordClose, []byte{0, 0, 0, 3, 0, 1, 1})
	if errSeal != nil {
		return nil, errSeal
	}

	e := &Exchange{request: request, clientTranscript: append(clientTranscript, noop...)}
	copy(e.psk[:], session.PSK)
	return e, nil
}

// RequestBody returns a defensive copy of the prepared record stream.
func (e *Exchange) RequestBody() []byte {
	if e == nil {
		return nil
	}
	return append([]byte(nil), e.request...)
}

// DecodeResponse validates the 0-RTT PSK response sequence recovered from a
// live anonymous QR request: plaintext ServerHello (0x16), encrypted Server
// Finished (0x16), encrypted business response (0x17), encrypted close
// (0x15). Every encrypted record is AEAD-opened in sequence. Server Finished
// is also HMAC-checked before a business payload is returned. This function
// performs no network activity.
func (e *Exchange) DecodeResponse(raw []byte) ([]byte, error) {
	if e == nil || len(e.clientTranscript) == 0 {
		return nil, errors.New("mmtls: exchange is not initialized")
	}
	records, err := splitRecords(raw)
	if err != nil {
		return nil, err
	}
	if len(records) != 4 || records[0].typ != recordServerHello ||
		records[1].typ != recordServerHello || records[2].typ != recordBusiness ||
		records[3].typ != recordClose {
		return nil, ErrMalformed
	}
	serverHello := records[0].payload
	if len(serverHello) == 0 {
		return nil, ErrMalformed
	}
	transcript := make([]byte, 0, len(e.clientTranscript)+len(serverHello))
	transcript = append(transcript, e.clientTranscript...)
	transcript = append(transcript, serverHello...)
	material := hkdfExpand(e.psk[:], append([]byte("handshake key expansion"), sha256Sum(transcript)...), 28)
	key, iv := material[:16], material[16:]

	finished, err := openRecord(key, iv, 1, records[1])
	if err != nil {
		return nil, err
	}
	if err := verifyServerFinished(e.psk[:], sha256Sum(transcript), finished); err != nil {
		return nil, err
	}
	business, err := openRecord(key, iv, 2, records[2])
	if err != nil {
		return nil, err
	}
	if len(business) == 0 {
		return nil, ErrMalformed
	}
	if _, err := openRecord(key, iv, 3, records[3]); err != nil {
		return nil, err
	}
	return append([]byte(nil), business...), nil
}

type record struct {
	typ     byte
	payload []byte
	header  [recordHeaderSize]byte
}

func validateSession(s Session) error {
	if len(s.PSK) != 32 {
		return errors.New("mmtls: PSK must be exactly 32 bytes")
	}
	if len(s.Ticket) < 4 || len(s.Ticket) > maxTicketSize {
		return errors.New("mmtls: ticket has invalid length")
	}
	if int(binary.BigEndian.Uint32(s.Ticket[:4])) != len(s.Ticket)-4 {
		return errors.New("mmtls: ticket length prefix does not match payload")
	}
	return nil
}

func validateHostPath(host, path string) error {
	if host == "" || len(host) > 32767 || !utf8.ValidString(host) || containsControl(host) {
		return errors.New("mmtls: invalid host")
	}
	if path == "" || !strings.HasPrefix(path, "/") || len(path) > 32767 || !utf8.ValidString(path) || containsControl(path) {
		return errors.New("mmtls: invalid path")
	}
	return nil
}

func containsControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func makeRequestData(host, path string, business []byte) ([]byte, error) {
	innerLen := 2 + len(path) + 2 + len(host) + 4 + len(business)
	if innerLen > maxRecordPayload-tagSize-4 {
		return nil, errors.New("mmtls: request envelope exceeds record limit")
	}
	b := make([]byte, 4, 4+innerLen)
	binary.BigEndian.PutUint32(b, uint32(innerLen))
	b = binary.BigEndian.AppendUint16(b, uint16(len(path)))
	b = append(b, path...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(host)))
	b = append(b, host...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(business)))
	b = append(b, business...)
	return b, nil
}

func makeClientHello(ticket, random []byte, now time.Time) ([]byte, error) {
	if len(random) != 32 {
		return nil, errors.New("mmtls: client random must be 32 bytes")
	}
	// Candidate layout observed in MmtlsEncryptData: inner uint32 length,
	// fixed hello prefix, random, Unix seconds, fixed extension bytes, ticket.
	const prefix = "\x01\x03\xf1\x01\x00\xa8"
	fixed := []byte{0, 0, 0, 0x6f, 1, 0, 0, 0, 0x6a, 0, 0x0f, 1}
	innerLen := len(prefix) + len(random) + 4 + len(fixed) + len(ticket)
	if innerLen+4 > maxRecordPayload {
		return nil, errors.New("mmtls: client hello exceeds record limit")
	}
	b := make([]byte, 4, innerLen+4)
	binary.BigEndian.PutUint32(b, uint32(innerLen))
	b = append(b, prefix...)
	b = append(b, random...)
	b = binary.BigEndian.AppendUint32(b, uint32(now.Unix()))
	b = append(b, fixed...)
	b = append(b, ticket...)
	return b, nil
}

func makeNoop(now time.Time) []byte {
	b := []byte{0, 0, 0, 0x10, 8, 0, 0, 0, 0x0b, 1, 0, 0, 0, 6, 0, 0x12}
	return binary.BigEndian.AppendUint32(b, uint32(now.Unix()))
}

func appendRecord(dst []byte, typ byte, payload []byte) []byte {
	dst = append(dst, typ, versionHi, versionLo)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(payload)))
	return append(dst, payload...)
}

func appendEncrypted(dst, key, iv []byte, seq uint64, typ byte, plain []byte) ([]byte, error) {
	if len(plain)+tagSize > maxRecordPayload {
		return nil, errors.New("mmtls: plaintext exceeds record limit")
	}
	header := [recordHeaderSize]byte{typ, versionHi, versionLo}
	binary.BigEndian.PutUint16(header[3:], uint16(len(plain)+tagSize))
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	aad := makeAAD(seq, header[:])
	ciphertext := gcm.Seal(nil, nonce(iv, seq), plain, aad)
	return append(dst, append(header[:], ciphertext...)...), nil
}

func splitRecords(raw []byte) ([]record, error) {
	if len(raw) == 0 {
		return nil, ErrMalformed
	}
	var out []record
	for len(raw) > 0 {
		if len(raw) < recordHeaderSize {
			return nil, ErrMalformed
		}
		var h [recordHeaderSize]byte
		copy(h[:], raw[:recordHeaderSize])
		if h[1] != versionHi || h[2] != versionLo {
			return nil, ErrMalformed
		}
		n := int(binary.BigEndian.Uint16(h[3:]))
		raw = raw[recordHeaderSize:]
		if n > len(raw) {
			return nil, ErrMalformed
		}
		out = append(out, record{typ: h[0], header: h, payload: append([]byte(nil), raw[:n]...)})
		raw = raw[n:]
	}
	return out, nil
}

func openRecord(key, iv []byte, seq uint64, r record) ([]byte, error) {
	if len(r.payload) < tagSize {
		return nil, ErrMalformed
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce(iv, seq), r.payload, makeAAD(seq, r.header[:]))
	if err != nil {
		return nil, ErrAuth
	}
	return plain, nil
}

func makeAAD(seq uint64, header []byte) []byte {
	b := make([]byte, 8+recordHeaderSize)
	binary.BigEndian.PutUint64(b, seq)
	copy(b[8:], header)
	return b
}
func nonce(iv []byte, seq uint64) []byte {
	n := append([]byte(nil), iv...)
	v := binary.BigEndian.Uint32(n[8:])
	binary.BigEndian.PutUint32(n[8:], v^uint32(seq))
	return n
}
func sha256Sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

// hkdfExpand is RFC 5869 expand with SHA-256. The sample's historical helper
// uses this expand-only construction; PSK is therefore treated as a PRK here.
func hkdfExpand(prk, info []byte, n int) []byte {
	out := make([]byte, 0, n)
	var t []byte
	for counter := byte(1); len(out) < n; counter++ {
		m := hmac.New(sha256.New, prk)
		m.Write(t)
		m.Write(info)
		m.Write([]byte{counter})
		t = m.Sum(nil)
		out = append(out, t...)
	}
	return out[:n]
}
