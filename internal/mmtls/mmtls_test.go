package mmtls

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func fixtureSession() Session {
	psk := bytes.Repeat([]byte{0x41}, 32)
	ticket := append([]byte{0, 0, 0, 3}, []byte{0xaa, 0xbb, 0xcc}...)
	return Session{PSK: psk, Ticket: ticket}
}

func TestExchangeRoundTripWithSyntheticPeer(t *testing.T) {
	now := time.Unix(1700000000, 0)
	e, err := NewExchange(fixtureSession(), "example.test", "/cgi-bin/x", []byte("request"), now, bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	request := e.RequestBody()
	records, err := splitRecords(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[0].typ != recordNoop || records[1].typ != recordNoop || records[2].typ != recordBusiness || records[3].typ != recordClose {
		t.Fatalf("unexpected records")
	}
	if !bytes.Equal(request, e.RequestBody()) {
		t.Fatal("request must be deterministic and copied")
	}
	transcript := append(append([]byte(nil), records[0].payload...), makeNoop(now)...)
	response := synthesizePeer(t, fixtureSession(), transcript, []byte("server hello"), []byte("reply"), true)
	got, err := e.DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "reply" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeRejectsTamperingTruncationAndWrongStructure(t *testing.T) {
	now := time.Unix(1700000000, 0)
	e, err := NewExchange(fixtureSession(), "example.test", "/x", []byte("request"), now, bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	records, _ := splitRecords(e.RequestBody())
	transcript := append(append([]byte(nil), records[0].payload...), makeNoop(now)...)
	good := synthesizePeer(t, fixtureSession(), transcript, []byte("hello"), []byte("reply"), true)
	tampered := append([]byte(nil), good...)
	tampered[len(tampered)-1] ^= 1
	if _, err := e.DecodeResponse(tampered); !errors.Is(err, ErrAuth) {
		t.Fatalf("tamper error=%v", err)
	}
	if _, err := e.DecodeResponse(good[:len(good)-1]); !errors.Is(err, ErrMalformed) {
		t.Fatalf("truncation error=%v", err)
	}
	noBusiness := synthesizePeer(t, fixtureSession(), transcript, []byte("hello"), nil, false)
	if _, err := e.DecodeResponse(noBusiness); !errors.Is(err, ErrMalformed) {
		t.Fatalf("no business error=%v", err)
	}
	duplicate := synthesizePeer(t, fixtureSession(), transcript, []byte("hello"), []byte("a"), true)
	// Add another record after the recovered fixed four-record response layout.
	duplicate = appendSecondBusiness(t, fixtureSession(), transcript, []byte("hello"), duplicate, []byte("b"))
	if _, err := e.DecodeResponse(duplicate); !errors.Is(err, ErrMalformed) {
		t.Fatalf("duplicate error=%v", err)
	}
	badFinished := synthesizePeerWithBadFinished(t, fixtureSession(), transcript, []byte("hello"), []byte("reply"))
	if _, err := e.DecodeResponse(badFinished); err == nil {
		t.Fatal("wrong but AEAD-valid server finished accepted")
	}
	unknown := append([]byte(nil), good...)
	unknown[recordHeaderSize+len([]byte("hello"))] = 0x42 // first encrypted record type after server hello
	if _, err := e.DecodeResponse(unknown); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unknown type error=%v", err)
	}
}

func TestInputBoundariesAndNonceVariation(t *testing.T) {
	now := time.Unix(1, 0)
	s := fixtureSession()
	bad := s
	bad.PSK = bad.PSK[:31]
	if _, err := NewExchange(bad, "h", "/x", nil, now, bytes.NewReader(make([]byte, 32))); err == nil {
		t.Fatal("short PSK accepted")
	}
	bad = s
	bad.Ticket = []byte{0, 0, 0, 4, 1}
	if _, err := NewExchange(bad, "h", "/x", nil, now, bytes.NewReader(make([]byte, 32))); err == nil {
		t.Fatal("bad ticket accepted")
	}
	if _, err := NewExchange(s, "bad\nhost", "/x", nil, now, bytes.NewReader(make([]byte, 32))); err == nil {
		t.Fatal("control host accepted")
	}
	if _, err := NewExchange(s, "h", "x", nil, now, bytes.NewReader(make([]byte, 32))); err == nil {
		t.Fatal("path accepted")
	}
	iv := bytes.Repeat([]byte{9}, 12)
	if bytes.Equal(nonce(iv, 1), nonce(iv, 2)) {
		t.Fatal("nonces repeat")
	}
	if _, err := NewExchange(s, "h", "/x", make([]byte, maxRecordPayload), now, bytes.NewReader(make([]byte, 32))); err == nil {
		t.Fatal("oversized business accepted")
	}
}

// synthesizePeer is a local independent record producer, not a live oracle.
func synthesizePeer(t *testing.T, s Session, clientTranscript, serverHello, business []byte, includeBusiness bool) []byte {
	return synthesizePeerWithFinished(t, s, clientTranscript, serverHello, business, includeBusiness, false)
}

func synthesizePeerWithBadFinished(t *testing.T, s Session, clientTranscript, serverHello, business []byte) []byte {
	return synthesizePeerWithFinished(t, s, clientTranscript, serverHello, business, true, true)
}

func synthesizePeerWithFinished(t *testing.T, s Session, clientTranscript, serverHello, business []byte, includeBusiness, badFinished bool) []byte {
	t.Helper()
	transcript := append(append([]byte(nil), clientTranscript...), serverHello...)
	material := hkdfExpand(s.PSK, append([]byte("handshake key expansion"), sha256Sum(transcript)...), 28)
	out := appendRecord(nil, recordServerHello, serverHello)
	finishedKey := hkdfExpand(s.PSK, []byte("server finished"), sha256.Size)
	mac := hmac.New(sha256.New, finishedKey)
	mac.Write(sha256Sum(transcript))
	finishedMAC := mac.Sum(nil)
	if badFinished {
		finishedMAC[0] ^= 1
	}
	finished := binary.BigEndian.AppendUint32(nil, uint32(3+len(finishedMAC)))
	finished = append(finished, 0x14)
	finished = binary.BigEndian.AppendUint16(finished, uint16(len(finishedMAC)))
	finished = append(finished, finishedMAC...)
	if includeBusiness {
		var err error
		out, err = appendEncrypted(out, material[:16], material[16:], 1, recordServerHello, finished)
		if err != nil {
			t.Fatal(err)
		}
		out, err = appendEncrypted(out, material[:16], material[16:], 2, recordBusiness, business)
		if err != nil {
			t.Fatal(err)
		}
		out, err = appendEncrypted(out, material[:16], material[16:], 3, recordClose, []byte{0, 0, 0, 3, 0, 1, 1})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	var err error
	out, err = appendEncrypted(out, material[:16], material[16:], 1, recordServerHello, finished)
	if err != nil {
		t.Fatal(err)
	}
	out, err = appendEncrypted(out, material[:16], material[16:], 2, recordClose, []byte{0, 0, 0, 3, 0, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func appendSecondBusiness(t *testing.T, s Session, clientTranscript, serverHello, existing, second []byte) []byte {
	t.Helper()
	material := hkdfExpand(s.PSK, append([]byte("handshake key expansion"), sha256Sum(append(append([]byte(nil), clientTranscript...), serverHello...))...), 28)
	out, err := appendEncrypted(existing, material[:16], material[16:], 4, recordBusiness, second)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Keep these imports and a direct GCM roundtrip here to make test failures
// distinguish package record errors from Go's AES-GCM implementation.
func TestSyntheticPeerUsesGCM(t *testing.T) {
	b, _ := aes.NewCipher(bytes.Repeat([]byte{1}, 16))
	g, _ := cipher.NewGCM(b)
	x := g.Seal(nil, bytes.Repeat([]byte{2}, 12), []byte("x"), []byte("a"))
	got, err := g.Open(nil, bytes.Repeat([]byte{2}, 12), x, []byte("a"))
	if err != nil || string(got) != "x" {
		t.Fatal("gcm fixture")
	}
	var check [4]byte
	binary.BigEndian.PutUint32(check[:], 1)
	if check[3] != 1 {
		t.Fatal("fixture")
	}
}
