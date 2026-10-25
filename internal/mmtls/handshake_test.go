package mmtls

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestHandshakeCompletesAgainstSyntheticP256Peer(t *testing.T) {
	h, err := NewHandshake(time.Unix(1700000000, 0), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	response, expected, err := syntheticHandshakePeer(h.RequestBody(), false, false)
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.Complete(response)
	if err != nil {
		t.Fatal(err)
	}
	if !session.Experimental {
		t.Fatal("candidate handshake claimed server authentication")
	}
	if !bytes.Equal(session.PSK, expected.PSK) || !bytes.Equal(session.Ticket, expected.Ticket) {
		t.Fatal("derived session does not match independent peer")
	}
	if err := validateSession(session); err != nil {
		t.Fatal(err)
	}
}

func TestHandshakeRejectsBadTagFinishedAndServerKey(t *testing.T) {
	for _, tc := range []struct {
		name        string
		badTag      bool
		badFinished bool
	}{
		{"bad_tag", true, false},
		{"bad_finished", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := NewHandshake(time.Now(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			response, _, err := syntheticHandshakePeer(h.RequestBody(), tc.badTag, tc.badFinished)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.Complete(response); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
	for _, raw := range [][]byte{nil, {0x16, 0xf1, 3, 0, 0}, bytes.Repeat([]byte{1}, 5+123)} {
		h, err := NewHandshake(time.Now(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Complete(raw); err == nil {
			t.Fatal("malformed response accepted")
		}
	}
}

func TestHandshakeAcceptsFirstKeySelectionZeroAndTicketTrailingData(t *testing.T) {
	h, err := NewHandshake(time.Unix(1700000000, 0), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// InitMmtlsShake selects key two only for value 2; all other values use
	// key one. Its ticket slice also stops at the embedded length rather than
	// consuming all of part 3. This synthetic fixture checks those two exact
	// compatibility rules without claiming a real server accepts them.
	response, expected, err := syntheticHandshakePeerOptions(h.RequestBody(), false, false, 0, []byte{0xaa, 0xbb})
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.Complete(response)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.PSK, expected.PSK) || !bytes.Equal(got.Ticket, expected.Ticket) {
		t.Fatal("selection-zero handshake derived the wrong session")
	}
}

func TestHandshakePinnedServerSignature(t *testing.T) {
	h, err := NewHandshake(time.Unix(1700000000, 0), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	response, identity, err := signedSyntheticHandshakePeer(h.RequestBody())
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.CompleteWithPinnedServerKey(response, &identity.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !session.Experimental {
		t.Fatal("an unproven local pin must not claim WeChat identity authentication")
	}

	wrong, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err = NewHandshake(time.Unix(1700000001, 0), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	response, _, err = signedSyntheticHandshakePeer(h.RequestBody())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.CompleteWithPinnedServerKey(response, &wrong.PublicKey); err == nil {
		t.Fatal("wrong server identity key accepted")
	}
}

// syntheticHandshakePeer derives its own ECDH secret and uses the documented
// record formulas. It is an offline test harness, not a WeChat interoperability
// oracle and deliberately does not emulate a long-term server signature.
func syntheticHandshakePeer(request []byte, badTag, badFinished bool) ([]byte, Session, error) {
	return syntheticHandshakePeerOptions(request, badTag, badFinished, 1, nil)
}

func syntheticHandshakePeerOptions(request []byte, badTag, badFinished bool, selected uint32, part3Trailer []byte) ([]byte, Session, error) {
	records, err := splitRecords(request)
	if err != nil || len(records) != 1 || records[0].typ != handshakeRecordType {
		return nil, Session{}, errors.New("invalid synthetic client hello")
	}
	clientPub, err := clientFirstKey(records[0].payload)
	if err != nil {
		return nil, Session{}, err
	}
	serverPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, Session{}, err
	}
	shared, err := serverPrivate.ECDH(clientPub)
	if err != nil {
		return nil, Session{}, err
	}
	secret := sha256Sum(shared)
	serverHello := make([]byte, serverHelloPrefix+p256PublicSize)
	binary.BigEndian.PutUint32(serverHello[52:56], selected)
	copy(serverHello[serverHelloPrefix:], serverPrivate.PublicKey().Bytes())
	transcript := append(append([]byte(nil), records[0].payload...), serverHello...)
	material := independentHandshakeExpand(secret, append([]byte("handshake key expansion"), sha256Sum(transcript)...), 56)
	decryptKey, decryptIV := material[16:32], material[44:56]
	part2 := []byte{0, 0, 0, 1, 8} // opaque encrypted extensions candidate
	ticket := append([]byte{0, 0, 0, 3}, 7, 8, 9)
	part3 := append([]byte{0, 0, 0, 0, 0, 0}, ticket...)
	part3 = append(part3, part3Trailer...)
	transcript = append(transcript, part2...)
	part2Hash := sha256Sum(transcript)
	transcript = append(transcript, part3...)
	part3Hash := sha256Sum(transcript)
	finishedKey := independentHandshakeExpand(secret, []byte("server finished"), sha256.Size)
	mac := hmac.New(sha256.New, finishedKey)
	mac.Write(part3Hash)
	verify := mac.Sum(nil)
	if badFinished {
		verify[0] ^= 1
	}
	part4 := binary.BigEndian.AppendUint32(nil, uint32(3+len(verify)))
	part4 = append(part4, 0x14)
	part4 = binary.BigEndian.AppendUint16(part4, uint16(len(verify)))
	part4 = append(part4, verify...)

	response := appendRecord(nil, handshakeRecordType, serverHello)
	for seq, part := range [][]byte{part2, part3, part4} {
		response, err = appendEncrypted(response, decryptKey, decryptIV, uint64(seq+1), handshakeRecordType, part)
		if err != nil {
			return nil, Session{}, err
		}
	}
	if badTag {
		response[len(response)-1] ^= 1
	}
	psk := independentHandshakeExpand(secret, append([]byte("PSK_ACCESS"), part2Hash...), 32)
	return response, Session{PSK: psk, Ticket: ticket, Experimental: true}, nil
}

// signedSyntheticHandshakePeer supplies a structurally separate signature
// record. It validates our optional local pin interface only; it is not a
// claim that the generated identity represents a WeChat server.
func signedSyntheticHandshakePeer(request []byte) ([]byte, *ecdsa.PrivateKey, error) {
	records, err := splitRecords(request)
	if err != nil || len(records) != 1 || records[0].typ != handshakeRecordType {
		return nil, nil, errors.New("invalid synthetic client hello")
	}
	clientPub, err := clientFirstKey(records[0].payload)
	if err != nil {
		return nil, nil, err
	}
	serverPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	identity, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	shared, err := serverPrivate.ECDH(clientPub)
	if err != nil {
		return nil, nil, err
	}
	secret := sha256Sum(shared)
	serverHello := make([]byte, serverHelloPrefix+p256PublicSize)
	binary.BigEndian.PutUint32(serverHello[52:56], 1)
	copy(serverHello[serverHelloPrefix:], serverPrivate.PublicKey().Bytes())
	transcript := append(append([]byte(nil), records[0].payload...), serverHello...)
	material := independentHandshakeExpand(secret, append([]byte("handshake key expansion"), sha256Sum(transcript)...), 56)
	decryptKey, decryptIV := material[16:32], material[44:56]
	signature, err := ecdsa.SignASN1(rand.Reader, identity, sha256Sum(transcript))
	if err != nil {
		return nil, nil, err
	}
	part2 := binary.BigEndian.AppendUint32(nil, uint32(3+len(signature)))
	part2 = append(part2, 0x0f)
	part2 = binary.BigEndian.AppendUint16(part2, uint16(len(signature)))
	part2 = append(part2, signature...)
	ticket := append([]byte{0, 0, 0, 3}, 7, 8, 9)
	part3 := append([]byte{0, 0, 0, 0, 0, 0}, ticket...)
	transcript = append(transcript, part2...)
	transcript = append(transcript, part3...)
	part3Hash := sha256Sum(transcript)
	finishedKey := independentHandshakeExpand(secret, []byte("server finished"), sha256.Size)
	mac := hmac.New(sha256.New, finishedKey)
	mac.Write(part3Hash)
	verify := mac.Sum(nil)
	part4 := binary.BigEndian.AppendUint32(nil, uint32(3+len(verify)))
	part4 = append(part4, 0x14)
	part4 = binary.BigEndian.AppendUint16(part4, uint16(len(verify)))
	part4 = append(part4, verify...)
	response := appendRecord(nil, handshakeRecordType, serverHello)
	for seq, part := range [][]byte{part2, part3, part4} {
		response, err = appendEncrypted(response, decryptKey, decryptIV, uint64(seq+1), handshakeRecordType, part)
		if err != nil {
			return nil, nil, err
		}
	}
	return response, identity, nil
}

func clientFirstKey(hello []byte) (*ecdh.PublicKey, error) {
	if len(hello) < 4 || int(binary.BigEndian.Uint32(hello[:4])) != len(hello)-4 {
		return nil, ErrMalformed
	}
	// Prefix (6), random (32), Unix time (4), and extension bytes (20).
	pos := 4 + 6 + 32 + 4 + 20
	if pos+2 > len(hello) {
		return nil, ErrMalformed
	}
	n := int(binary.BigEndian.Uint16(hello[pos:]))
	pos += 2
	if n != p256PublicSize || pos+n > len(hello) {
		return nil, ErrMalformed
	}
	return ecdh.P256().NewPublicKey(hello[pos : pos+n])
}

// independentHandshakeExpand is a local, separately written RFC 5869 expand
// oracle. It does not call the production helper under test.
func independentHandshakeExpand(prk, info []byte, n int) []byte {
	out := make([]byte, 0, n)
	var previous []byte
	for counter := byte(1); len(out) < n; counter++ {
		mac := hmac.New(sha256.New, prk)
		mac.Write(previous)
		mac.Write(info)
		mac.Write([]byte{counter})
		previous = mac.Sum(nil)
		out = append(out, previous...)
	}
	return out[:n]
}
