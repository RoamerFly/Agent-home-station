package business

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
)

func sampleSession() Session {
	return Session{ClientVersion: 0x28004039, UIN: 12345, SessionKey: bytes.Repeat([]byte{1}, 16), Cookie: bytes.Repeat([]byte{2}, 15), LoginSalt: []byte("synthetic-login-salt")}
}

func TestInitializationOperationKeepsFramingAndRejectsUnknownCGI(t *testing.T) {
	s := sampleSession()
	sign := func([]byte) (uint32, error) { return 1, nil }
	text, err := EncodeText([]byte("same plaintext"), s, sign)
	if err != nil {
		t.Fatal(err)
	}
	init, err := EncodeOperation([]byte("same plaintext"), s, InitCGI, sign)
	if err != nil {
		t.Fatal(err)
	}
	cgi, n := binary.Uvarint(init[26:])
	if n <= 0 || cgi != 139 {
		t.Fatal("wrong initialization CGI")
	}
	if !bytes.Equal(text[text[1]>>2:], init[init[1]>>2:]) {
		t.Fatal("CGI changed business ciphertext")
	}
	if _, err = EncodeOperation([]byte("same plaintext"), s, 140, sign); err == nil {
		t.Fatal("unrecovered CGI framing accepted")
	}
}

func TestEnvelopeHeaderAndCiphertext(t *testing.T) {
	s := sampleSession()
	payload := []byte("hello")
	var signed []byte
	p, err := EncodeText(payload, s, func(body []byte) (uint32, error) { signed = append([]byte(nil), body...); return 0x11223344, nil })
	if err != nil {
		t.Fatal(err)
	}
	if p[0] != 0xbf || p[1]&3 != 2 || p[2] != 0x5f || binary.BigEndian.Uint32(p[3:7]) != s.ClientVersion || binary.BigEndian.Uint32(p[7:11]) != s.UIN {
		t.Fatal("wrong fixed header")
	}
	if !bytes.Equal(p[11:26], s.Cookie) {
		t.Fatal("wrong cookie")
	}
	cgi, n := binary.Uvarint(p[26:])
	if n <= 0 || cgi != 522 {
		t.Fatal("wrong CGI")
	}
	// AES-CBC known answer obtained independently using WSL OpenSSL enc:
	// aes-128-cbc, key=IV=16 bytes of 01, input="hello", default PKCS#7 padding.
	want, _ := hex.DecodeString("d580b16cac462f16aadfa9b56af748cb")
	if !bytes.Equal(signed, want) {
		t.Fatalf("CBC mismatch: %x", signed)
	}
	if !bytes.Equal(p[int(p[1]>>2):], signed) {
		t.Fatal("signature did not cover the transmitted ciphertext")
	}
	got, cookie, err := Decode(p, s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) || !bytes.Equal(cookie, s.Cookie) {
		t.Fatal("decoded data mismatch")
	}
}

func TestDecodeRejectsHeaderMismatchAndBadPadding(t *testing.T) {
	s := sampleSession()
	p, err := EncodeText([]byte("hello"), s, func([]byte) (uint32, error) { return 1, nil })
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func([]byte){
		"magic": func(b []byte) { b[0] = 0 }, "header-too-short": func(b []byte) { b[1] = 2 }, "invalid-mode": func(b []byte) { b[1] &= 0xfc },
		"wrong-uin": func(b []byte) { b[8] ^= 1 }, "wrong-encryption": func(b []byte) { b[2] = 0x6f },
		"ciphertext-corrupt": func(b []byte) { b[len(b)-1] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), p...)
			change(b)
			if _, _, err := Decode(b, s); err == nil {
				t.Fatal("accepted invalid packet")
			}
		})
	}
	for n := 0; n < len(p); n++ {
		if _, _, err := Decode(p[:n], s); err == nil {
			t.Fatalf("accepted truncated length %d", n)
		}
	}
}

func TestSigningFailureStopsPacket(t *testing.T) {
	want := errors.New("sign failed")
	if _, err := EncodeText([]byte{1}, sampleSession(), func([]byte) (uint32, error) { return 0, want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte{0xbf, 0, 0})
	s := sampleSession()
	f.Fuzz(func(t *testing.T, b []byte) { _, _, _ = Decode(b, s) })
}
