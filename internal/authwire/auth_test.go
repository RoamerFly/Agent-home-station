package authwire

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"testing"
)

func t32(v uint32) *uint32 { return &v }
func ti32(v int32) *int32  { return &v }
func ts(v string) *string  { return &v }

func TestBuildRequestUsesRecoveredFields(t *testing.T) {
	_, pub, err := GenerateP224()
	if err != nil {
		t.Fatal(err)
	}
	in := RequestInput{Base: BaseRequest{SessionKey: []byte("0123456789012345"), HasSessionKey: true, UIN: t32(0), DeviceID: []byte{0x49, 1}, HasDeviceID: true, ClientVersion: ti32(402668320), DeviceType: []byte("iPad Air iPadOS18.4.1"), HasDeviceType: true, Scene: t32(1)}, RandomKey: []byte("1234567890123456"), ClientECDHPublic: pub, UserName: "wx-test", Password: Secret("temporary"), Device: DeviceFields{DeviceName: ts("iPad"), ExtSpamInfo: &Buffer{Length: t32(3), Data: []byte{1, 2, 3}, HasData: true}}}
	got, err := BuildRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	var top []int
	if err := fields(got, 0, func(f field) error { top = append(top, f.n); return nil }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte{1, 2}, ints(top)) {
		t.Fatalf("top-level fields=%v", top)
	}
	// Confirm RSA field 2 is ECDHKey and uses descriptor's nid=713.
	var rsa []byte
	_ = fields(got, 0, func(f field) error {
		if f.n == 1 {
			rsa = f.b
		}
		return nil
	})
	var nid uint64
	_ = fields(rsa, 1, func(f field) error {
		if f.n == 2 {
			_ = fields(f.b, 2, func(g field) error {
				if g.n == 1 {
					nid = g.v
				}
				return nil
			})
		}
		return nil
	})
	if nid != 713 {
		t.Fatalf("ECDH nid=%d", nid)
	}
	if got := Secret("s").String(); got != "[redacted]" {
		t.Fatal(got)
	}
}
func ints(v []int) []byte {
	o := make([]byte, len(v))
	for i, n := range v {
		o[i] = byte(n)
	}
	return o
}

func TestParseResponseAndPresence(t *testing.T) {
	buf := func(x string) []byte {
		return marshalBuffer(Buffer{Length: t32(uint32(len(x))), Data: []byte(x), HasData: true})
	}
	ecdh := marshalECDH(ECDHKey{NID: ti32(713), Key: &Buffer{Length: t32(2), Data: []byte{4, 5}, HasData: true}})
	auth := av(nil, 1, 7)
	auth = ab(auth, 2, ecdh)
	auth = ab(auth, 3, buf("ss"))
	auth = ab(auth, 4, buf("aa"))
	auth = ab(auth, 23, buf("cc"))
	auth = ab(auth, 24, buf("dd"))
	account := ab(nil, 1, []byte("wx"))
	account = ab(account, 2, []byte("nick"))
	account = av(account, 8, 1)
	host := ab(nil, 1, []byte("short.example"))
	host = ab(host, 2, []byte("redirect.example"))
	hosts := ab(nil, 2, host)
	network := ab(nil, 1, hosts)
	raw := ab(nil, 1, av(nil, 1, 0))
	raw = av(raw, 2, 3)
	raw = ab(raw, 3, auth)
	raw = ab(raw, 4, account)
	raw = ab(raw, 5, network)
	raw = av(raw, 99, 1)
	r, err := ParseResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c, e := r.Code(); e != nil || c != 0 {
		t.Fatalf("code %d %v", c, e)
	}
	if r.UIN == nil || *r.UIN != 7 || r.EncryptedSessionKey == nil || string(r.EncryptedSessionKey.Data) != "ss" || r.ServerECDH == nil || r.Account == nil || r.Account.NickName == nil || len(r.Hosts) != 1 || r.Hosts[0].Host == nil {
		t.Fatalf("parse result %#v", r)
	}
	missing, err := ParseResponse(av(nil, 99, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.Code(); !errors.Is(err, ErrMissing) {
		t.Fatalf("absent ret became success: %v", err)
	}
}

func TestP224AndStrictSessionDecrypt(t *testing.T) {
	aPriv, aPub, err := GenerateP224()
	if err != nil {
		t.Fatal(err)
	}
	bPriv, bPub, err := GenerateP224()
	if err != nil {
		t.Fatal(err)
	}
	ka, err := AuthorizationAESKey(aPriv, bPub)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := AuthorizationAESKey(bPriv, aPub)
	if err != nil || !bytes.Equal(ka, kb) {
		t.Fatalf("keys differ: %x %x %v", ka, kb, err)
	}
	plain := []byte("session-material")
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(ka)
	ct := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, ka).CryptBlocks(ct, plain)
	got, err := DecryptSessionKey(aPriv, bPub, ct)
	if err != nil || string(got) != "session-material" {
		t.Fatalf("decrypt %q %v", got, err)
	}
	// CBC does not authenticate ciphertext: a random bit flip can happen to
	// produce valid padding. Encrypt a known invalid pad to test strict PKCS#7
	// rejection deterministically, without claiming integrity protection.
	badPlain := append([]byte(nil), plain...)
	badPlain[len(badPlain)-1] = 0
	badCiphertext := make([]byte, len(badPlain))
	cipher.NewCBCEncrypter(block, ka).CryptBlocks(badCiphertext, badPlain)
	if _, err := DecryptSessionKey(aPriv, bPub, badCiphertext); !errors.Is(err, ErrInvalidPadding) {
		t.Fatalf("tamper err=%v", err)
	}
	legacy, err := AuthorizationAESKeyLegacyUnpadded(aPriv, bPub)
	if err != nil {
		t.Fatal(err)
	}
	block, _ = aes.NewCipher(legacy)
	ct = make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, legacy).CryptBlocks(ct, plain)
	got, err = DecryptSessionKeyLegacyUnpadded(aPriv, bPub, ct)
	if err != nil || string(got) != "session-material" {
		t.Fatalf("legacy decrypt %q %v", got, err)
	}
}
