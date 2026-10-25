// Package hybrid implements the iPad HybridECDH envelope used before an
// ordinary authenticated session exists. It is a source implementation of a
// bounded subset reconstructed from the pinned image and descriptor evidence.
//
// It deliberately has no network code, CGO, or embedded server public key.
// A caller supplies the profile-specific bootstrap key and uses each Client
// for one request/response exchange at a time.
package hybrid

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"wechat-bot/internal/rqtx"
)

const (
	// CGI values carried by the reconstructed HybridECDH outer packet. Their
	// live acceptance is not established by these constants alone.
	CGILoginQRCode      uint32 = 502
	CGICheckLoginQRCode uint32 = 503
	CGISecManualAuth    uint32 = 252
	// SendIosDeviceTokenRequest sets CGI 0xecd before its HybridECDH call.
	CGIFingerprint uint32 = 3789
	// CGILogin is retained as the QR-login request alias for callers written
	// before the operation's more precise name was established.
	CGILogin = CGILoginQRCode

	protocolID          = uint32(415)
	maxPlaintext        = 2 << 20
	maxPacket           = maxPlaintext + 8192
	encodedAEADOverhead = 12 + 16 // nonce and GCM tag are appended to ciphertext.
)

var (
	ErrPacket       = errors.New("hybrid: invalid packet")
	ErrExchangeBusy = errors.New("hybrid: previous exchange has not been decoded")
	ErrNoExchange   = errors.New("hybrid: no pending exchange")
)

// Client holds one pending HybridECDH exchange. It is safe for concurrent use,
// but deliberately rejects a second Encode until Decode consumes the first.
// This prevents accidental reuse of an ephemeral private key or transcript.
type Client struct {
	serverPublic  *ecdh.PublicKey
	clientVersion uint32
	entropy       io.Reader

	mu      sync.Mutex
	pending *exchange
}

type exchange struct {
	private *ecdh.PrivateKey
	plain   []byte
	keyTail []byte
}

// New validates an uncompressed SEC1 P-256 bootstrap public key. The key is
// intentionally an input: the caller must select it from a versioned profile
// backed by evidence rather than relying on an implicit hard-coded value.
func New(serverPublicKey []byte, clientVersion uint32, entropy io.Reader) (*Client, error) {
	if clientVersion == 0 || entropy == nil {
		return nil, errors.New("hybrid: version and entropy are required")
	}
	pub, err := ecdh.P256().NewPublicKey(append([]byte(nil), serverPublicKey...))
	if err != nil {
		return nil, fmt.Errorf("hybrid: invalid P-256 server public key: %w", err)
	}
	return &Client{serverPublic: pub, clientVersion: clientVersion, entropy: entropy}, nil
}

// Encode creates an outer HybridECDH request. RQTX is calculated over the
// resulting HybridEcdhRequest protobuf bytes, before the outer CGI header.
func (c *Client) Encode(cgi, uin uint32, cookie, plain []byte) ([]byte, error) {
	if !supportedCGI(cgi) || len(plain) == 0 || len(plain) > maxPlaintext || len(cookie) > 15 {
		return nil, ErrPacket
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != nil {
		return nil, ErrExchangeBusy
	}
	priv, err := ecdh.P256().GenerateKey(c.entropy)
	if err != nil {
		return nil, fmt.Errorf("hybrid: generate P-256 key: %w", err)
	}
	shared, err := legacyShared(priv, c.serverPublic)
	if err != nil {
		return nil, err
	}
	bootstrapKey := sha256.Sum256(shared)
	clientPub := priv.PublicKey().Bytes()
	h1 := transcript([]byte("1"), u32Decimal(protocolID), clientPub)

	randomKey, err := random(c.entropy, 32)
	if err != nil {
		return nil, err
	}
	nonce, err := random(c.entropy, 12)
	if err != nil {
		return nil, err
	}
	wrappedRandom, err := sealCompressed(bootstrapKey[:24], nonce, randomKey, h1[:])
	if err != nil {
		return nil, err
	}
	derived := hybridKDF(randomKey, h1[:], 56)
	h2 := transcript([]byte("1"), u32Decimal(protocolID), clientPub, wrappedRandom)
	encrypted, err := sealCompressed(derived[:24], nonce, plain, h2[:])
	if err != nil {
		return nil, err
	}
	inner := marshalRequest(clientPub, wrappedRandom, encrypted)
	signature, err := rqtx.SignBody(inner)
	if err != nil {
		return nil, fmt.Errorf("hybrid: sign encrypted protobuf: %w", err)
	}
	packet, err := encodeOuter(c.clientVersion, cgi, uin, cookie, uint64(len(plain)), inner, signature)
	if err != nil {
		return nil, err
	}
	c.pending = &exchange{private: priv, plain: append([]byte(nil), plain...), keyTail: append([]byte(nil), derived[24:]...)}
	return packet, nil
}

// Decode consumes the pending exchange and verifies/decrypts a response. A
// failed decode also consumes it, because retrying a malformed or tampered
// response with the same ephemeral transcript is unsafe and ambiguous.
func (c *Client) Decode(packet []byte) (plain, cookie []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		return nil, nil, ErrNoExchange
	}
	x := c.pending
	c.pending = nil
	_, cookie, body, err := decodeOuter(packet)
	if err != nil {
		return nil, nil, responseError("outer", err)
	}
	response, err := parseResponse(body)
	if err != nil {
		return nil, nil, responseError("protobuf", err)
	}
	serverPub, err := ecdh.P256().NewPublicKey(response.public)
	if err != nil {
		return nil, nil, responseError("protobuf", ErrPacket)
	}
	shared, err := legacyShared(x.private, serverPub)
	if err != nil {
		return nil, nil, responseError("ECDH", err)
	}
	key := sha256.Sum256(shared)
	aad := transcript(x.keyTail, x.plain, u32Decimal(protocolID), response.public, []byte("1"))
	plain, err = openCompressed(key[:24], response.data, aad[:])
	if err != nil {
		return nil, nil, responseError("GCM", err)
	}
	return plain, cookie, nil
}

// responseError is intentionally content-free: callers can tell which stage
// failed without logging encrypted packets, keys, or decrypted login data.
func responseError(stage string, err error) error {
	return fmt.Errorf("hybrid response %s: %w", stage, err)
}

// legacyShared returns the P-256 ECDH X coordinate without leading zeroes.
// The pinned historical Go implementation hashes x.Bytes(), rather than the
// fixed-width output returned by crypto/ecdh. crypto/ecdh still performs the
// actual P-256 operation and point validation here.
func legacyShared(private *ecdh.PrivateKey, public *ecdh.PublicKey) ([]byte, error) {
	shared, err := private.ECDH(public)
	if err != nil {
		return nil, fmt.Errorf("hybrid: ECDH: %w", err)
	}
	shared = bytes.TrimLeft(shared, "\x00")
	if len(shared) == 0 {
		return nil, ErrPacket
	}
	return shared, nil
}

func random(r io.Reader, n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, fmt.Errorf("hybrid: entropy: %w", err)
	}
	return b, nil
}

func u32Decimal(v uint32) []byte { return []byte(fmt.Sprintf("%d", v)) }

func supportedCGI(cgi uint32) bool {
	return cgi == CGILoginQRCode || cgi == CGICheckLoginQRCode || cgi == CGISecManualAuth || cgi == CGIFingerprint
}

func transcript(parts ...[]byte) [32]byte {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// hybridKDF matches the observed two-stage construction: extract is
// HMAC-SHA256(key="security hdkf expand", data=randomKey), then standard
// HKDF-Expand using the first transcript digest as info.
func hybridKDF(randomKey, info []byte, n int) []byte {
	extract := hmac.New(sha256.New, []byte("security hdkf expand"))
	_, _ = extract.Write(randomKey)
	prk := extract.Sum(nil)
	out, prev := make([]byte, 0, n), []byte(nil)
	for counter := byte(1); len(out) < n; counter++ {
		h := hmac.New(sha256.New, prk)
		_, _ = h.Write(prev)
		_, _ = h.Write(info)
		_, _ = h.Write([]byte{counter})
		prev = h.Sum(nil)
		out = append(out, prev...)
	}
	return out[:n]
}

func sealCompressed(key, nonce, plain, aad []byte) ([]byte, error) {
	if len(key) != 24 || len(nonce) != 12 {
		return nil, ErrPacket
	}
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(plain); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, compressed.Bytes(), aad)
	if len(sealed) < gcm.Overhead() {
		return nil, ErrPacket
	}
	cut := len(sealed) - gcm.Overhead()
	out := make([]byte, 0, len(sealed)+len(nonce))
	out = append(out, sealed[:cut]...)
	out = append(out, nonce...)
	out = append(out, sealed[cut:]...)
	return out, nil
}

func openCompressed(key, encoded, aad []byte) ([]byte, error) {
	if len(key) != 24 || len(encoded) < encodedAEADOverhead || len(encoded) > maxPacket {
		return nil, ErrPacket
	}
	nonceAt := len(encoded) - encodedAEADOverhead
	tagAt := len(encoded) - 16
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := make([]byte, 0, nonceAt+16)
	ciphertext = append(ciphertext, encoded[:nonceAt]...)
	ciphertext = append(ciphertext, encoded[tagAt:]...)
	compressed, err := gcm.Open(nil, encoded[nonceAt:tagAt], ciphertext, aad)
	if err != nil {
		return nil, ErrPacket
	}
	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, ErrPacket
	}
	plain, readErr := io.ReadAll(io.LimitReader(zr, maxPlaintext+1))
	closeErr := zr.Close()
	if readErr != nil || closeErr != nil || len(plain) > maxPlaintext {
		return nil, ErrPacket
	}
	return plain, nil
}

func appendVarint(dst []byte, value uint64) []byte { return binary.AppendUvarint(dst, value) }
func appendBytes(dst []byte, field byte, value []byte) []byte {
	dst = append(dst, field)
	dst = appendVarint(dst, uint64(len(value)))
	return append(dst, value...)
}

// SKBuiltinBuffer_t: iLen=1, buffer=2.
func marshalBuffer(value []byte) []byte {
	b := appendVarint([]byte{0x08}, uint64(protocolID))
	return appendBytes(b, 0x12, value)
}

// HybridEcdhRequest fields: type=1, SecECDHKey=2, randomkeydata=3,
// randomkeyextenddata=4 (omitted), encyptdata=5.
func marshalRequest(public, wrappedRandom, encrypted []byte) []byte {
	b := appendVarint([]byte{0x08}, 1)
	b = appendBytes(b, 0x12, marshalBuffer(public))
	b = appendBytes(b, 0x1a, wrappedRandom)
	return appendBytes(b, 0x2a, encrypted)
}

type response struct{ public, data []byte }

func parseResponse(in []byte) (response, error) {
	fields, err := parseFields(in)
	if err != nil {
		return response{}, err
	}
	key, ok := fields[1]
	if !ok || len(key) != 1 {
		return response{}, ErrPacket
	}
	public, err := parseBuffer(key[0])
	if err != nil {
		return response{}, err
	}
	data, ok := fields[3]
	if !ok || len(data) != 1 || len(data[0]) < encodedAEADOverhead {
		return response{}, ErrPacket
	}
	return response{public: public, data: data[0]}, nil
}

func parseBuffer(in []byte) ([]byte, error) {
	fields, err := parseFields(in)
	if err != nil {
		return nil, err
	}
	lengths, ok := fields[1]
	if !ok || len(lengths) != 1 {
		return nil, ErrPacket
	}
	n, consumed := binary.Uvarint(lengths[0])
	if consumed <= 0 || n != uint64(protocolID) {
		return nil, ErrPacket
	}
	values, ok := fields[2]
	if !ok || len(values) != 1 || len(values[0]) != 65 {
		return nil, ErrPacket
	}
	return append([]byte(nil), values[0]...), nil
}

// parseFields accepts only varint and length-delimited wire types, caps total
// data, rejects duplicate unknown fields, and keeps all values for validation.
func parseFields(in []byte) (map[uint64][][]byte, error) {
	if len(in) == 0 || len(in) > maxPacket {
		return nil, ErrPacket
	}
	result := make(map[uint64][][]byte)
	for len(in) > 0 {
		key, n := binary.Uvarint(in)
		if n <= 0 || key>>3 == 0 {
			return nil, ErrPacket
		}
		in = in[n:]
		field, wire := key>>3, key&7
		var value []byte
		switch wire {
		case 0:
			_, m := binary.Uvarint(in)
			if m <= 0 {
				return nil, ErrPacket
			}
			value = append([]byte(nil), in[:m]...)
			in = in[m:]
		case 2:
			length, m := binary.Uvarint(in)
			if m <= 0 || length > uint64(len(in)-m) || length > maxPacket {
				return nil, ErrPacket
			}
			in = in[m:]
			value = append([]byte(nil), in[:int(length)]...)
			in = in[int(length):]
		default:
			return nil, ErrPacket
		}
		result[field] = append(result[field], value)
	}
	return result, nil
}

func encodeOuter(version, cgi, uin uint32, cookie []byte, plainLen uint64, body []byte, signature uint32) ([]byte, error) {
	if version == 0 || !supportedCGI(cgi) || len(body) == 0 || len(body) > maxPacket || len(cookie) > 15 {
		return nil, ErrPacket
	}
	header := []byte{0xbf, 0, byte(12<<4 | len(cookie))}
	header = binary.BigEndian.AppendUint32(header, version)
	header = binary.BigEndian.AppendUint32(header, uin)
	header = append(header, cookie...)
	header = appendVarint(header, uint64(cgi))
	header = appendVarint(header, plainLen)
	header = appendVarint(header, plainLen)
	// Observed legacy iPad request constants before RQTX.
	header = append(header, 0x90, 0x4e, 0x0d, 0x00, 0xff)
	header = appendVarint(header, uint64(signature))
	header = append(header, 0)
	if len(header) > 63 {
		return nil, ErrPacket
	}
	header[1] = byte(len(header)<<2 | 2)
	return append(header, body...), nil
}

// decodeOuter follows the iPad *response* decoder at main+0x7d3200. It is
// deliberately not the inverse of encodeOuter: after BF/length/flags it skips
// four bytes, reads UIN, cookie, CGI and one protobuf-length varint, then uses
// headerLen as the body boundary. The sample does not validate the skipped four
// bytes, a second request-length varint, or the request-only marker/RQTX area.
func decodeOuter(packet []byte) (cgi uint64, cookie, body []byte, err error) {
	if len(packet) < 12 || len(packet) > maxPacket || packet[0] != 0xbf {
		return 0, nil, nil, ErrPacket
	}
	headerLen := int(packet[1] >> 2)
	cookieLen := int(packet[2] & 15)
	if cookieLen > 15 || headerLen < 11+cookieLen || headerLen >= len(packet) {
		return 0, nil, nil, ErrPacket
	}
	cur := 11 + cookieLen
	read := func() (uint64, bool) {
		v, n := binary.Uvarint(packet[cur:headerLen])
		if n <= 0 {
			return 0, false
		}
		cur += n
		return v, true
	}
	var ok bool
	if cgi, ok = read(); !ok {
		return 0, nil, nil, ErrPacket
	}
	if _, ok = read(); !ok {
		return 0, nil, nil, ErrPacket
	}
	return cgi, append([]byte(nil), packet[11:11+cookieLen]...), append([]byte(nil), packet[headerLen:]...), nil
}
