package native

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"testing"

	"wechat-bot/internal/business"
	"wechat-bot/internal/sending"
)

// These are deliberately independent test helpers. They are a synthetic peer,
// not a claim that this candidate implementation interoperates with WeChat.
const (
	mmtlsNoop     = 0x19
	mmtlsBusiness = 0x17
	mmtlsHello    = 0x16
)

type testRecord struct {
	typ     byte
	header  []byte
	payload []byte
}

func TestSendTextSyntheticMMTLSPeer(t *testing.T) {
	t.Run("validated acknowledgement", func(t *testing.T) {
		s, calls := testSenderWithPeer(t, peerModeGood)
		receipt, err := s.SendText(context.Background(), sending.TextRequest{Recipient: "filehelper", Text: "local integration"})
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Recipient != "filehelper" || receipt.ClientMessageID == "" || receipt.ServerMessageID != "987654" {
			t.Fatalf("unexpected receipt: %#v", receipt)
		}
		if *calls != 1 {
			t.Fatalf("calls=%d, want 1", *calls)
		}
	})

	for _, mode := range []peerMode{peerModeBadAck, peerModeBadTag} {
		mode := mode
		t.Run(mode.String(), func(t *testing.T) {
			s, calls := testSenderWithPeer(t, mode)
			_, err := s.SendText(context.Background(), sending.TextRequest{Recipient: "filehelper", Text: "must not retry"})
			if !errors.Is(err, sending.ErrOutcomeUnknown) {
				t.Fatalf("error=%v, want outcome unknown", err)
			}
			if *calls != 1 {
				t.Fatalf("calls=%d: send was retried", *calls)
			}
		})
	}
}

type peerMode byte

const (
	peerModeGood peerMode = iota
	peerModeBadAck
	peerModeBadTag
)

func (p peerMode) String() string {
	if p == peerModeBadAck {
		return "malformed_ack_is_not_accepted"
	}
	if p == peerModeBadTag {
		return "bad_mmtls_tag_is_not_accepted"
	}
	return "good"
}

func testSenderWithPeer(t *testing.T, mode peerMode) (*sender, *int) {
	t.Helper()
	e, err := New(writeSession(t, syntheticSession()))
	if err != nil {
		t.Fatal(err)
	}
	s := e.(*sender)
	calls := 0
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != s.session.ShortHost || r.URL.Path == "" || r.Method != http.MethodPost {
			t.Fatal("unexpected synthetic peer request target")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		response, err := syntheticResponse(s.session, raw, mode)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(response)), Request: r}, nil
	})}
	return s, &calls
}

func syntheticResponse(s SessionFile, raw []byte, mode peerMode) ([]byte, error) {
	records, err := parseTestRecords(raw)
	if err != nil || len(records) != 4 || records[0].typ != mmtlsNoop || records[1].typ != mmtlsNoop || records[2].typ != mmtlsBusiness {
		return nil, errors.New("unexpected request record layout")
	}
	clientHello := records[0].payload
	early, err := independentExpand(s.MMTLSPSK, append([]byte("early data key expansion"), sum(clientHello)...), 28)
	if err != nil {
		return nil, err
	}
	noop, err := openTestRecord(early[:16], early[16:], 1, records[1])
	if err != nil {
		return nil, err
	}
	wrapped, err := openTestRecord(early[:16], early[16:], 2, records[2])
	if err != nil {
		return nil, err
	}
	packet, err := decodeRequestEnvelope(wrapped, s.ShortHost, textPath)
	if err != nil {
		return nil, err
	}
	pb, _, err := business.Decode(packet, business.Session{ClientVersion: s.ClientVersion, UIN: s.UIN, SessionKey: s.SessionKey, Cookie: s.Cookie, LoginSalt: s.LoginSalt})
	if err != nil {
		return nil, err
	}
	recipient, text, clientID, err := parseTextRequest(pb)
	if err != nil || recipient != "filehelper" || text == "" || clientID == 0 {
		return nil, errors.New("synthetic peer could not validate recovered text protobuf")
	}

	var ack []byte
	if mode == peerModeBadAck {
		ack = []byte{0x08, 0x01} // not a valid NewSendMsgResponse acknowledgement
	} else {
		ack = marshalAcknowledgement(recipient, clientID, 987654)
	}
	responsePacket, err := business.EncodeText(ack, business.Session{ClientVersion: s.ClientVersion, UIN: s.UIN, SessionKey: s.SessionKey, Cookie: s.Cookie, LoginSalt: s.LoginSalt}, func([]byte) (uint32, error) { return 0, nil })
	if err != nil {
		return nil, err
	}
	serverHello := []byte("synthetic-server-hello")
	transcript := append(append([]byte(nil), clientHello...), noop...)
	transcript = append(transcript, serverHello...)
	handshake, err := independentExpand(s.MMTLSPSK, append([]byte("handshake key expansion"), sum(transcript)...), 28)
	if err != nil {
		return nil, err
	}
	response := appendTestRecord(nil, mmtlsHello, serverHello)
	finishedKey, err := independentExpand(s.MMTLSPSK, []byte("server finished"), sha256.Size)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, finishedKey)
	mac.Write(sum(transcript))
	finishedMAC := mac.Sum(nil)
	finished := binary.BigEndian.AppendUint32(nil, uint32(3+len(finishedMAC)))
	finished = append(finished, 0x14)
	finished = binary.BigEndian.AppendUint16(finished, uint16(len(finishedMAC)))
	finished = append(finished, finishedMAC...)
	response, err = appendTestEncrypted(response, handshake[:16], handshake[16:], 1, mmtlsHello, finished)
	if err != nil {
		return nil, err
	}
	response, err = appendTestEncrypted(response, handshake[:16], handshake[16:], 2, mmtlsBusiness, responsePacket)
	if err != nil {
		return nil, err
	}
	response, err = appendTestEncrypted(response, handshake[:16], handshake[16:], 3, 0x15, []byte{0, 0, 0, 3, 0, 1, 1})
	if err != nil {
		return nil, err
	}
	if mode == peerModeBadTag {
		response[len(response)-1] ^= 1
	}
	return response, nil
}

func decodeRequestEnvelope(raw []byte, host, path string) ([]byte, error) {
	if len(raw) < 4 || int(binary.BigEndian.Uint32(raw)) != len(raw)-4 {
		return nil, errors.New("invalid synthetic request envelope")
	}
	raw = raw[4:]
	readString := func() (string, error) {
		if len(raw) < 2 {
			return "", errors.New("truncated synthetic request envelope")
		}
		n := int(binary.BigEndian.Uint16(raw))
		raw = raw[2:]
		if n > len(raw) {
			return "", errors.New("truncated synthetic request envelope")
		}
		v := string(raw[:n])
		raw = raw[n:]
		return v, nil
	}
	gotPath, err := readString()
	if err != nil {
		return nil, err
	}
	gotHost, err := readString()
	if err != nil {
		return nil, err
	}
	if gotPath != path || gotHost != host || len(raw) < 4 {
		return nil, errors.New("wrong synthetic request route")
	}
	n := int(binary.BigEndian.Uint32(raw))
	raw = raw[4:]
	if n != len(raw) {
		return nil, errors.New("wrong synthetic request business length")
	}
	return append([]byte(nil), raw...), nil
}

// parseTextRequest decodes only fields established by the recovered request
// schema. It is intentionally independent from the production encoder.
func parseTextRequest(b []byte) (recipient, text string, id uint64, err error) {
	fields, err := testFields(b)
	if err != nil {
		return "", "", 0, err
	}
	for _, f := range fields {
		if f.number != 2 || f.wire != 2 {
			continue
		}
		chat, e := testFields(f.bytes)
		if e != nil {
			return "", "", 0, e
		}
		for _, c := range chat {
			switch c.number {
			case 1:
				nested, e := testFields(c.bytes)
				if e != nil || len(nested) != 1 || nested[0].number != 1 || nested[0].wire != 2 {
					return "", "", 0, errors.New("invalid recipient")
				}
				recipient = string(nested[0].bytes)
			case 2:
				text = string(c.bytes)
			case 5:
				id = c.value
			}
		}
	}
	if recipient == "" || text == "" || id == 0 {
		return "", "", 0, errors.New("required text fields absent")
	}
	return recipient, text, id, nil
}

func marshalAcknowledgement(recipient string, clientID, serverID uint64) []byte {
	base := appendVarint(nil, 1, 0)
	item := appendVarint(nil, 1, 0)
	item = appendBytes(item, 2, appendBytes(nil, 1, []byte(recipient)))
	item = appendVarint(item, 4, clientID)
	item = appendVarint(item, 8, serverID)
	out := appendBytes(nil, 1, base)
	out = appendVarint(out, 2, 1)
	return appendBytes(out, 3, item)
}

type testField struct {
	number int
	wire   uint64
	value  uint64
	bytes  []byte
}

func testFields(b []byte) ([]testField, error) {
	var out []testField
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errors.New("bad protobuf tag")
		}
		b = b[n:]
		f := testField{number: int(tag >> 3), wire: tag & 7}
		if f.number < 1 {
			return nil, errors.New("bad protobuf field")
		}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errors.New("bad protobuf int")
			}
			f.value = v
			b = b[n:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return nil, errors.New("bad protobuf bytes")
			}
			b = b[n:]
			f.bytes = b[:l]
			b = b[l:]
		default:
			return nil, errors.New("unsupported protobuf wire")
		}
		out = append(out, f)
	}
	return out, nil
}
func appendVarint(b []byte, n int, v uint64) []byte {
	b = binary.AppendUvarint(b, uint64(n<<3))
	return binary.AppendUvarint(b, v)
}
func appendBytes(b []byte, n int, v []byte) []byte {
	b = binary.AppendUvarint(b, uint64(n<<3|2))
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

func parseTestRecords(raw []byte) ([]testRecord, error) {
	var out []testRecord
	for len(raw) > 0 {
		if len(raw) < 5 || raw[1] != 0xf1 || raw[2] != 3 {
			return nil, errors.New("bad test record")
		}
		n := int(binary.BigEndian.Uint16(raw[3:5]))
		if len(raw) < 5+n {
			return nil, errors.New("truncated test record")
		}
		out = append(out, testRecord{raw[0], append([]byte(nil), raw[:5]...), append([]byte(nil), raw[5:5+n]...)})
		raw = raw[5+n:]
	}
	return out, nil
}
func appendTestRecord(dst []byte, typ byte, payload []byte) []byte {
	dst = append(dst, typ, 0xf1, 3)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(payload)))
	return append(dst, payload...)
}
func testNonce(iv []byte, seq uint64) []byte {
	n := append([]byte(nil), iv...)
	binary.BigEndian.PutUint32(n[8:], binary.BigEndian.Uint32(n[8:])^uint32(seq))
	return n
}
func testAAD(seq uint64, header []byte) []byte {
	a := make([]byte, 13)
	binary.BigEndian.PutUint64(a, seq)
	copy(a[8:], header)
	return a
}
func openTestRecord(key, iv []byte, seq uint64, r testRecord) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return g.Open(nil, testNonce(iv, seq), r.payload, testAAD(seq, r.header))
}
func appendTestEncrypted(dst, key, iv []byte, seq uint64, typ byte, plain []byte) ([]byte, error) {
	h := []byte{typ, 0xf1, 3, 0, 0}
	binary.BigEndian.PutUint16(h[3:], uint16(len(plain)+16))
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return append(dst, append(h, g.Seal(nil, testNonce(iv, seq), plain, testAAD(seq, h))...)...), nil
}
func sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }
func independentExpand(prk, info []byte, n int) ([]byte, error) {
	return hkdf.Expand(sha256.New, prk, string(info), n)
}
