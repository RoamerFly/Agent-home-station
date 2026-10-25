package sae08

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestStaticSampleModel(t *testing.T) {
	data, err := os.ReadFile("../../research/samples/08sae.dat.bin")
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParsePinned(data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../research/evidence/sae08-static-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Vectors []struct {
			Plain string `json:"plain_hex"`
			Zip   string `json:"zip_hex"`
			ZT    string `json:"zt_hex"`
		} `json:"vectors"`
	}
	if err = json.Unmarshal(b, &evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence.Vectors) != 11 {
		t.Fatal("missing static vectors")
	}
	for _, v := range evidence.Vectors {
		p, _ := hex.DecodeString(v.Plain)
		want, _ := hex.DecodeString(v.Zip)
		got, err := c.EncodeZipData(p)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("len %d: got %x, err %v; want %x", len(p), got, err, want)
		}
		again, _ := c.EncodeZipData(p)
		if !bytes.Equal(got, again) {
			t.Fatal("state leaked across calls")
		}
		wantZT, _ := hex.DecodeString(v.ZT)
		gotZT, err := c.EncryptZT(p)
		if err != nil || !bytes.Equal(gotZT, wantZT) {
			t.Fatalf("ZT len %d mismatch %x, %v", len(p), gotZT, err)
		}
	}
	data[0] ^= 1
	if _, err := ParsePinned(data); err == nil {
		t.Fatal("accepted changed pinned artifact")
	}
}

// Build unmasked lookup tables encoding ordinary AES, then compare the recovered
// table pipeline against the independent standard-library AES implementation.
// The fixed first vector is also NIST SP800-38A F.2.1's first CBC block.
func TestPipelineAgainstStandardAES(t *testing.T) {
	key, _ := hex.DecodeString("2b7e151628aed2a6abf7158809cf4f3c")
	iv, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	c := aesTables(key, iv)
	block, _ := aes.NewCipher(key)
	for _, n := range []int{0, 1, 15, 16, 17, 31, 32, 33, 64, 1024} {
		p := make([]byte, n)
		for i := range p {
			p[i] = byte(i*73 + 19)
		}
		want := append([]byte(nil), p...)
		for len(want)%16 != 0 {
			want = append(want, byte(16-n%16))
		}
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(want, want)
		got, err := c.EncodeZipData(p)
		if n > 0 && (err != nil || !bytes.Equal(got, want)) {
			t.Fatalf("AES pipeline n=%d got=%x want=%x err=%v", n, got, want, err)
		}
		padded := bytes.Clone(p)
		padding := 16 - n%16
		padded = append(padded, bytes.Repeat([]byte{byte(padding)}, padding)...)
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(padded, padded)
		gotZT, err := c.EncryptZT(p)
		if err != nil || !bytes.Equal(gotZT, padded) {
			t.Fatalf("ZT AES n=%d mismatch %x %v", n, gotZT, err)
		}
	}
	p, _ := hex.DecodeString("6bc1bee22e409f96e93d7e117393172a")
	got, _ := c.EncodeZipData(p)
	if hex.EncodeToString(got) != "7649abac8119b246cee98e9b12e9197d" {
		t.Fatalf("NIST CBC mismatch %x", got)
	}
}

func TestErrorsAndOwnership(t *testing.T) {
	for _, b := range [][]byte{nil, {0x80}, {0}, {0x0a, 0xff, 0xff, 0xff, 0xff, 0x7f}, {0x08, 1}, {0x12, 0}, {0xa0, 3, 1, 0xa0, 3, 1}} {
		if _, err := Parse(b); err == nil {
			t.Fatalf("accepted malformed %x", b)
		}
	}
	c := aesTables(make([]byte, 16), make([]byte, 16))
	for _, p := range [][]byte{nil, make([]byte, MaxPlaintext+1)} {
		if _, err := c.EncodeZipData(p); err != ErrPlaintext {
			t.Fatalf("got %v", err)
		}
	}
	var invalid *Cipher
	if _, err := invalid.EncodeZipData([]byte{1}); err != ErrTables {
		t.Fatal(err)
	}
	p := []byte{1, 2, 3}
	before := bytes.Clone(p)
	_, _ = c.EncodeZipData(p)
	if !bytes.Equal(before, p) {
		t.Fatal("input mutated")
	}
	data, err := os.ReadFile("../../research/samples/08sae.dat.bin")
	if err != nil {
		t.Fatal(err)
	}
	x, err := ParsePinned(data)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := x.EncodeZipData(p)
	clear(data)
	got, _ := x.EncodeZipData(p)
	if !bytes.Equal(got, want) {
		t.Fatal("tables alias caller memory")
	}
}

func gf(a, b byte) byte {
	var out byte
	for b != 0 {
		if b&1 != 0 {
			out ^= a
		}
		high := a & 128
		a <<= 1
		if high != 0 {
			a ^= 0x1b
		}
		b >>= 1
	}
	return out
}
func sub(x byte) byte {
	var inv byte
	if x != 0 {
		inv = 1
		for i := 0; i < 254; i++ {
			inv = gf(inv, x)
		}
	}
	rot := func(x byte, n uint) byte { return x<<n | x>>(8-n) }
	return inv ^ rot(inv, 1) ^ rot(inv, 2) ^ rot(inv, 3) ^ rot(inv, 4) ^ 0x63
}

func aesTables(key, iv []byte) *Cipher {
	rk := make([]byte, 176)
	copy(rk, key)
	rc := byte(1)
	for n := 16; n < len(rk); n += 4 {
		tmp := [4]byte{rk[n-4], rk[n-3], rk[n-2], rk[n-1]}
		if n%16 == 0 {
			tmp = [4]byte{sub(tmp[1]) ^ rc, sub(tmp[2]), sub(tmp[3]), sub(tmp[0])}
			rc = gf(rc, 2)
		}
		for i := 0; i < 4; i++ {
			rk[n+i] = rk[n+i-16] ^ tmp[i]
		}
	}
	c := &Cipher{key: make([]byte, 147456), value: make([]byte, 110592), final: make([]byte, 4096)}
	copy(c.iv[:], iv)
	mix := [4][4]byte{{2, 3, 1, 1}, {1, 2, 3, 1}, {1, 1, 2, 3}, {3, 1, 1, 2}}
	for r := 0; r < 9; r++ {
		for row := 0; row < 4; row++ {
			for col := 0; col < 4; col++ {
				for x := 0; x < 256; x++ {
					b := byte(x)
					if r == 0 {
						b ^= rk[((col+row)%4)*4+row]
					}
					b = sub(b)
					for outrow := 0; outrow < 4; outrow++ {
						v := gf(mix[outrow][row], b)
						if row == 0 {
							v ^= rk[(r+1)*16+col*4+outrow]
						}
						c.key[r*16384+(row*4+col)*1024+x*4+outrow] = v
					}
				}
			}
		}
	}
	for pos := 0; pos < 9*16; pos++ {
		for stage := 0; stage < 3; stage++ {
			for half := 0; half < 2; half++ {
				for x := 0; x < 128; x++ {
					xor := func(i int) byte { return byte((i >> 4) ^ (i & 15)) }
					c.value[pos*768+stage*256+half*128+x] = xor(x) | (xor(x+128) << 4)
				}
			}
		}
	}
	for row := 0; row < 4; row++ {
		for col := 0; col < 4; col++ {
			for x := 0; x < 256; x++ {
				c.final[(row*4+col)*256+x] = sub(byte(x)) ^ rk[160+col*4+row]
			}
		}
	}
	return c
}
