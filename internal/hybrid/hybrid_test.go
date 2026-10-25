package hybrid

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"wechat-bot/internal/rqtx"
)

func TestExchangeRoundTripAndSingleUse(t *testing.T) {
	serverPrivate, err := ecdh.P256().GenerateKey(bytes.NewReader(bytes.Repeat([]byte{7}, 256)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(serverPrivate.PublicKey().Bytes(), 402668320, bytes.NewReader(bytes.Repeat([]byte{9}, 1024)))
	if err != nil {
		t.Fatal(err)
	}
	request, err := c.Encode(CGILogin, 1234, bytes.Repeat([]byte{1}, 15), []byte("send this text"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Encode(CGILogin, 1234, nil, []byte("again")); !errorsIs(err, ErrExchangeBusy) {
		t.Fatalf("second Encode error = %v", err)
	}
	response := syntheticResponse(t, serverPrivate, request, 402668320, []byte("server acknowledgement"))
	plain, cookie, err := c.Decode(response)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, []byte("server acknowledgement")) {
		t.Fatalf("plain=%q", plain)
	}
	if len(cookie) != 15 {
		t.Fatalf("cookie len=%d", len(cookie))
	}
	if _, _, err := c.Decode(response); !errorsIs(err, ErrNoExchange) {
		t.Fatalf("second Decode error = %v", err)
	}
}

func TestRejectsBadTagBadKeyAndTruncation(t *testing.T) {
	serverPrivate, err := ecdh.P256().GenerateKey(bytes.NewReader(bytes.Repeat([]byte{3}, 256)))
	if err != nil {
		t.Fatal(err)
	}
	makeClient := func(key []byte) *Client {
		c, e := New(key, 402668320, bytes.NewReader(bytes.Repeat([]byte{5}, 1024)))
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	for _, mutation := range []func([]byte) []byte{
		func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		func(b []byte) []byte { return b[:len(b)-1] },
	} {
		c := makeClient(serverPrivate.PublicKey().Bytes())
		req, e := c.Encode(CGICheckLoginQRCode, 1, nil, []byte("a"))
		if e != nil {
			t.Fatal(e)
		}
		resp := syntheticResponse(t, serverPrivate, req, 402668320, []byte("ok"))
		if _, _, e = c.Decode(mutation(resp)); e == nil {
			t.Fatal("accepted malformed response")
		}
	}
	wrong, e := ecdh.P256().GenerateKey(bytes.NewReader(bytes.Repeat([]byte{4}, 256)))
	if e != nil {
		t.Fatal(e)
	}
	c := makeClient(serverPrivate.PublicKey().Bytes())
	req, e := c.Encode(CGILogin, 1, nil, []byte("a"))
	if e != nil {
		t.Fatal(e)
	}
	resp := syntheticResponse(t, serverPrivate, req, 402668320, []byte("ok"))
	resp = replaceResponsePublic(t, resp, 402668320, wrong.PublicKey().Bytes())
	if _, _, e = c.Decode(resp); e == nil {
		t.Fatal("accepted wrong server key")
	}
}

func replaceResponsePublic(t *testing.T, packet []byte, version uint32, public []byte) []byte {
	t.Helper()
	cgi, cookie, body, err := decodeOuter(packet)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := parseFields(body)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := appendBytes(nil, 0x0a, marshalBuffer(public))
	rewritten = appendVarint(rewritten, 0x10)
	rewritten = appendVarint(rewritten, 1)
	rewritten = appendBytes(rewritten, 0x1a, fields[3][0])
	return encodeResponseOuter(uint32(cgi), 1, cookie, rewritten)
}

func TestNewRejectsInvalidKey(t *testing.T) {
	if _, err := New([]byte{4, 1}, 1, bytes.NewReader(make([]byte, 64))); err == nil {
		t.Fatal("accepted invalid key")
	}
}

func TestEncodePreservesSixByteCookie(t *testing.T) {
	server, err := ecdh.P256().GenerateKey(bytes.NewReader(bytes.Repeat([]byte{11}, 256)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(server.PublicKey().Bytes(), 402668320, bytes.NewReader(bytes.Repeat([]byte{12}, 1024)))
	if err != nil {
		t.Fatal(err)
	}
	cookie := []byte{1, 2, 3, 4, 5, 6}
	packet, err := c.Encode(CGICheckLoginQRCode, 0, cookie, []byte("poll"))
	if err != nil {
		t.Fatal(err)
	}
	if got := int(packet[2] & 0x0f); got != len(cookie) {
		t.Fatalf("cookie length nibble=%d", got)
	}
	if !bytes.Equal(packet[11:11+len(cookie)], cookie) {
		t.Fatal("cookie bytes were not preserved")
	}
}

// syntheticResponse is a local protocol peer for layer tests. It proves neither
// server behavior nor a valid WeChat login; it only independently reconstructs
// the reverse of the request cryptographic envelope.
func syntheticResponse(t *testing.T, serverPrivate *ecdh.PrivateKey, packet []byte, version uint32, responsePlain []byte) []byte {
	t.Helper()
	_, cookie, body, err := decodeOuter(packet)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := parseFields(body)
	if err != nil {
		t.Fatal(err)
	}
	clientPubRaw, err := parseBuffer(fields[2][0])
	if err != nil {
		t.Fatal(err)
	}
	clientPub, err := ecdh.P256().NewPublicKey(clientPubRaw)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := legacyShared(serverPrivate, clientPub)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := sha256.Sum256(shared)
	h1 := transcript([]byte("1"), u32Decimal(protocolID), clientPubRaw)
	randomKey, err := openCompressed(bootstrap[:24], fields[3][0], h1[:])
	if err != nil {
		t.Fatal(err)
	}
	derived := hybridKDF(randomKey, h1[:], 56)
	h2 := transcript([]byte("1"), u32Decimal(protocolID), clientPubRaw, fields[3][0])
	requestPlain, err := openCompressed(derived[:24], fields[5][0], h2[:])
	if err != nil {
		t.Fatal(err)
	}
	serverPub := serverPrivate.PublicKey().Bytes()
	responseShared, err := legacyShared(serverPrivate, clientPub)
	if err != nil {
		t.Fatal(err)
	}
	responseKey := sha256.Sum256(responseShared)
	aad := transcript(derived[24:], requestPlain, u32Decimal(protocolID), serverPub, []byte("1"))
	nonce := bytes.Repeat([]byte{0x33}, 12)
	encoded, err := sealCompressed(responseKey[:24], nonce, responsePlain, aad[:])
	if err != nil {
		t.Fatal(err)
	}
	response := appendBytes(nil, 0x0a, marshalBuffer(serverPub))
	response = appendVarint(response, 0x10)
	response = appendVarint(response, 1)
	response = appendBytes(response, 0x1a, encoded)
	return encodeResponseOuter(CGILogin, 1, cookie, response)
}

func errorsIs(err, target error) bool { return err == target }

func TestOuterCarriesRQTXOverEncryptedProtobuf(t *testing.T) {
	body := []byte{1, 2, 3}
	sig, err := rqtx.SignBody(body)
	if err != nil {
		t.Fatal(err)
	}
	p, err := encodeOuter(1, CGILoginQRCode, 0, nil, 3, body, sig)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(p[3:7]) != 1 {
		t.Fatal("version placement changed")
	}
	_, _, got, err := decodeOuter(p)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("decode: %v %x", err, got)
	}
}

// encodeResponseOuter deliberately does not reuse encodeOuter. The pinned
// iPad response parser only relies on this shared prefix and header boundary;
// request-only version/marker/RQTX fields must not leak into the fixture.
func encodeResponseOuter(cgi, uin uint32, cookie, body []byte) []byte {
	header := []byte{0xbf, 0, byte(len(cookie))}
	// The sample skips these four bytes in its response parser. They are kept
	// independent from the request client-version field.
	header = append(header, 0, 0, 0, 0)
	header = binary.BigEndian.AppendUint32(header, uin)
	header = append(header, cookie...)
	header = appendVarint(header, uint64(cgi))
	header = appendVarint(header, uint64(len(body)))
	// Deliberately different from the request marker/RQTX suffix.
	header = append(header, 0xa5)
	header[1] = byte(len(header)<<2 | 1)
	return append(header, body...)
}
