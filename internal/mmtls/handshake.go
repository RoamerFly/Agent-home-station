package mmtls

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	handshakeRecordType = 0x16
	serverHelloPrefix   = 58
	p256PublicSize      = 65
)

// Handshake constructs a candidate initial MMTLS exchange. It is intentionally
// offline: callers choose if and how its RequestBody is transported.
type Handshake struct {
	request     []byte
	clientHello []byte
	key1        *ecdh.PrivateKey
	key2        *ecdh.PrivateKey
}

// NewHandshake creates two fresh P-256 key shares and a candidate client hello.
// entropy is injectable so callers can test deterministically. It must be
// cryptographically secure when used outside tests.
func NewHandshake(now time.Time, entropy io.Reader) (*Handshake, error) {
	if entropy == nil {
		return nil, errors.New("mmtls: handshake entropy is required")
	}
	curve := ecdh.P256()
	key1, err := curve.GenerateKey(entropy)
	if err != nil {
		return nil, fmt.Errorf("mmtls: generate first P-256 key: %w", err)
	}
	key2, err := curve.GenerateKey(entropy)
	if err != nil {
		return nil, fmt.Errorf("mmtls: generate second P-256 key: %w", err)
	}
	var random [32]byte
	if _, err := io.ReadFull(entropy, random[:]); err != nil {
		return nil, fmt.Errorf("mmtls: read client random: %w", err)
	}
	clientHello, err := makeHandshakeHello(key1.PublicKey().Bytes(), key2.PublicKey().Bytes(), random[:], now)
	if err != nil {
		return nil, err
	}
	return &Handshake{request: appendRecord(nil, handshakeRecordType, clientHello), clientHello: clientHello, key1: key1, key2: key2}, nil
}

// RequestBody returns a defensive copy of the candidate client-hello record.
func (h *Handshake) RequestBody() []byte {
	if h == nil {
		return nil
	}
	return append([]byte(nil), h.request...)
}

// Complete verifies the recoverable response stages and returns material suited
// to NewExchange. Returned sessions are explicitly Experimental because this
// sample did not establish a recoverable, pinned server long-term verification
// key. A successfully checked finished MAC authenticates possession of the
// negotiated ephemeral secret, but does not authenticate a WeChat server.
func (h *Handshake) Complete(response []byte) (Session, error) {
	return h.complete(response, nil)
}

// CompleteWithPinnedServerKey performs the same cryptographic and framing
// checks as Complete, plus an ECDSA ASN.1 signature check of the transcript
// accumulated through ServerHello. The supplied key is an application pin: the
// caller, not this package, is responsible for establishing its provenance.
// Consequently the returned Session stays Experimental even on success.
func (h *Handshake) CompleteWithPinnedServerKey(response []byte, key *ecdsa.PublicKey) (Session, error) {
	if key == nil || key.Curve == nil || key.X == nil || key.Y == nil || !key.Curve.IsOnCurve(key.X, key.Y) {
		return Session{}, errors.New("mmtls: invalid pinned server public key")
	}
	return h.complete(response, key)
}

func (h *Handshake) complete(response []byte, pinnedServerKey *ecdsa.PublicKey) (Session, error) {
	if h == nil || h.key1 == nil || h.key2 == nil || len(h.clientHello) == 0 {
		return Session{}, errors.New("mmtls: handshake is not initialized")
	}
	records, err := splitRecords(response)
	if err != nil {
		return Session{}, handshakeStageError("record framing", err)
	}
	if len(records) != 4 || records[0].typ != handshakeRecordType {
		return Session{}, handshakeStageError("record layout", ErrMalformed)
	}
	for _, r := range records[1:] {
		// The initial-handshake reference labels server handshake records 0x16.
		// Short-request response types are a separate, later protocol phase.
		if r.typ != handshakeRecordType || len(r.payload) < tagSize {
			return Session{}, handshakeStageError("encrypted record layout", ErrMalformed)
		}
	}
	serverHello := records[0].payload
	serverKey, selected, err := h.serverKey(serverHello)
	if err != nil {
		return Session{}, handshakeStageError("server hello", err)
	}
	private := h.key1
	if selected == 2 {
		private = h.key2
	}
	shared, err := private.ECDH(serverKey)
	if err != nil || len(shared) != 32 {
		return Session{}, handshakeStageError("P-256 ECDH", errors.New("mmtls: invalid P-256 shared secret"))
	}
	secret := sha256Sum(shared)
	transcript := append(append([]byte(nil), h.clientHello...), serverHello...)
	keyMaterial := hkdfExpand(secret, append([]byte("handshake key expansion"), sha256Sum(transcript)...), 56)
	decryptKey, decryptIV := keyMaterial[16:32], keyMaterial[44:56]

	plain := make([][]byte, 3)
	for i := range plain {
		plain[i], err = openRecord(decryptKey, decryptIV, uint64(i+1), records[i+1])
		if err != nil {
			return Session{}, handshakeStageError(fmt.Sprintf("encrypted record %d", i+2), err)
		}
	}
	if pinnedServerKey != nil {
		if err := verifyServerSignature(pinnedServerKey, sha256Sum(transcript), plain[0]); err != nil {
			return Session{}, handshakeStageError("server signature", err)
		}
	}
	transcript = append(transcript, plain[0]...)
	part2Hash := sha256Sum(transcript)
	transcript = append(transcript, plain[1]...)
	part3Hash := sha256Sum(transcript)
	if err := verifyServerFinished(secret, part3Hash, plain[2]); err != nil {
		return Session{}, handshakeStageError("server finished", err)
	}
	ticket, err := ticketFromPart3(plain[1])
	if err != nil {
		return Session{}, handshakeStageError("session ticket", err)
	}
	psk := hkdfExpand(secret, append([]byte("PSK_ACCESS"), part2Hash...), 32)
	return Session{PSK: psk, Ticket: ticket, Experimental: true}, nil
}

func handshakeStageError(stage string, err error) error {
	return fmt.Errorf("mmtls: handshake %s: %w", stage, err)
}

func (h *Handshake) serverKey(serverHello []byte) (*ecdh.PublicKey, uint32, error) {
	if len(serverHello) != serverHelloPrefix+p256PublicSize {
		return nil, 0, ErrMalformed
	}
	selected := binary.BigEndian.Uint32(serverHello[52:56])
	// The pinned sample selects the second key only when this value is 2;
	// every other value takes the first key. In particular, do not reject zero
	// merely because the historical source did not name it: retaining that
	// branch lets an anonymous structural probe reach AEAD verification.
	if selected > 2 {
		return nil, 0, ErrMalformed
	}
	key, err := ecdh.P256().NewPublicKey(serverHello[serverHelloPrefix:])
	if err != nil {
		return nil, 0, errors.New("mmtls: invalid server P-256 public key")
	}
	return key, selected, nil
}

func makeHandshakeHello(pub1, pub2, random []byte, now time.Time) ([]byte, error) {
	if len(pub1) != p256PublicSize || len(pub2) != p256PublicSize || len(random) != 32 {
		return nil, errors.New("mmtls: invalid handshake key or random size")
	}
	// Candidate layout observed in InitMmtlsShake: inner uint32 length, hello
	// prefix, random, Unix seconds, extension prefix, two P-256 shares.
	b := []byte{1, 3, 0xf1, 1, 0xc0, 0x2b}
	b = append(b, random...)
	b = binary.BigEndian.AppendUint32(b, uint32(now.Unix()))
	b = append(b, 0, 0, 0, 0xa2, 1, 0, 0, 0, 0x9d, 0, 0x10, 2, 0, 0, 0, 0x47, 0, 0, 0, 1)
	b = binary.BigEndian.AppendUint16(b, uint16(len(pub1)))
	b = append(b, pub1...)
	b = append(b, 0, 0, 0, 0x47, 0, 0, 0, 2)
	b = binary.BigEndian.AppendUint16(b, uint16(len(pub2)))
	b = append(b, pub2...)
	b = append(b, 0, 0, 0, 1)
	if len(b)+4 > maxRecordPayload {
		return nil, errors.New("mmtls: client hello exceeds record limit")
	}
	out := binary.BigEndian.AppendUint32(nil, uint32(len(b)))
	out = append(out, b...)
	return out, nil
}

func ticketFromPart3(part3 []byte) ([]byte, error) {
	if len(part3) < 10 {
		return nil, ErrMalformed
	}
	n := int(binary.BigEndian.Uint32(part3[6:10]))
	// The historical routine copies only the declared early-data segment and
	// does not require it to consume the decrypted record.  Retain that exact
	// boundary: bytes after the opaque ticket are not interpreted here, while
	// a length that runs beyond this AEAD-authenticated plaintext is rejected.
	if n < 1 || n > maxTicketSize-4 || 10+n > len(part3) {
		return nil, ErrMalformed
	}
	ticket := append([]byte(nil), part3[6:10+n]...)
	if err := validateSession(Session{PSK: make([]byte, 32), Ticket: ticket}); err != nil {
		return nil, ErrMalformed
	}
	return ticket, nil
}

func verifyServerFinished(secret, transcriptHash, plain []byte) error {
	mac, err := finishedMAC(plain)
	if err != nil {
		return err
	}
	key := hkdfExpand(secret, []byte("server finished"), len(mac))
	expected := hmac.New(sha256.New, key)
	expected.Write(transcriptHash)
	if !hmac.Equal(mac, expected.Sum(nil)) {
		return errors.New("mmtls: server finished verification failed")
	}
	return nil
}

func finishedMAC(plain []byte) ([]byte, error) {
	// The historical initial-handshake routine calculated this MAC but ignored
	// comparison. The typed envelope is independently documented by a newer
	// protocol reference: uint32 total, byte type 0x14, uint16 MAC length, MAC.
	if len(plain) < 7 || int(binary.BigEndian.Uint32(plain[:4])) != len(plain)-4 || plain[4] != 0x14 {
		return nil, ErrMalformed
	}
	n := int(binary.BigEndian.Uint16(plain[5:7]))
	if n != sha256.Size || n != len(plain)-7 {
		return nil, ErrMalformed
	}
	return append([]byte(nil), plain[7:]...), nil
}

func verifyServerSignature(key *ecdsa.PublicKey, transcriptHash, plain []byte) error {
	// The candidate signature envelope is uint32 total || 0x0f || uint16
	// ASN.1-DER-length || signature.  It is independently described by the
	// public Go reference and is deliberately only enabled when the caller has
	// supplied a pin.
	if len(plain) < 7 || int(binary.BigEndian.Uint32(plain[:4])) != len(plain)-4 || plain[4] != 0x0f {
		return ErrMalformed
	}
	n := int(binary.BigEndian.Uint16(plain[5:7]))
	if n < 8 || n != len(plain)-7 {
		return ErrMalformed
	}
	if !ecdsa.VerifyASN1(key, transcriptHash, plain[7:]) {
		return errors.New("mmtls: server signature verification failed")
	}
	return nil
}

// DefaultHandshakeEntropy is exposed for callers that need the system CSPRNG
// without importing crypto/rand only to pass it into NewHandshake.
var DefaultHandshakeEntropy io.Reader = rand.Reader
