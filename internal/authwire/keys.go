package authwire

import (
	"crypto/aes"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"errors"
	"math/big"
)

var ErrInvalidKey = errors.New("invalid P-224 key material")
var ErrInvalidPadding = errors.New("invalid PKCS#7 padding")

// GenerateP224 returns a scalar and standard SEC1 uncompressed point (0x04||X||Y).
func GenerateP224() ([]byte, []byte, error) {
	c := elliptic.P224()
	n := c.Params().N
	for {
		d, err := rand.Int(rand.Reader, n)
		if err != nil {
			return nil, nil, err
		}
		if d.Sign() == 0 {
			continue
		}
		x, y := c.ScalarBaseMult(d.Bytes())
		return d.Bytes(), elliptic.Marshal(c, x, y), nil
	}
}

// SharedXPadded validates peer's SEC1 point then returns the P-224 shared X
// encoded as exactly 28 big-endian bytes. This differs from big.Int.Bytes(),
// which strips leading zeroes. The fixed 28-byte representation is what the
// historical reference hashes for the authorization MD5 key.
func SharedXPadded(priv, peer []byte) ([]byte, error) {
	c := elliptic.P224()
	d := new(big.Int).SetBytes(priv)
	if d.Sign() <= 0 || d.Cmp(c.Params().N) >= 0 {
		return nil, ErrInvalidKey
	}
	x, y := elliptic.Unmarshal(c, peer)
	if x == nil || y == nil || !c.IsOnCurve(x, y) {
		return nil, ErrInvalidKey
	}
	sx, _ := c.ScalarMult(x, y, priv)
	if sx == nil {
		return nil, ErrInvalidKey
	}
	out := make([]byte, 28)
	raw := sx.Bytes()
	if len(raw) > len(out) {
		return nil, ErrInvalidKey
	}
	copy(out[len(out)-len(raw):], raw)
	return out, nil
}
func AuthorizationAESKey(priv, peer []byte) ([]byte, error) {
	x, err := SharedXPadded(priv, peer)
	if err != nil {
		return nil, err
	}
	d := md5.Sum(x)
	return d[:], nil
}

// AuthorizationAESKeyLegacyUnpadded hashes sharedX.Bytes() directly. This
// reproduces the historical CheckSecManualAuth helper's representation. It is
// intentionally separate from AuthorizationAESKey because the two differ only
// when X has leading zero octets, yet then derive incompatible AES keys.
func AuthorizationAESKeyLegacyUnpadded(priv, peer []byte) ([]byte, error) {
	c := elliptic.P224()
	d := new(big.Int).SetBytes(priv)
	if d.Sign() <= 0 || d.Cmp(c.Params().N) >= 0 {
		return nil, ErrInvalidKey
	}
	x, y := elliptic.Unmarshal(c, peer)
	if x == nil || y == nil || !c.IsOnCurve(x, y) {
		return nil, ErrInvalidKey
	}
	sx, _ := c.ScalarMult(x, y, priv)
	if sx == nil {
		return nil, ErrInvalidKey
	}
	digest := md5.Sum(sx.Bytes())
	return digest[:], nil
}

// DecryptSessionKey decrypts the authorization response's AES-CBC payload with
// key=IV=MD5(fixed-width shared X) and requires valid PKCS#7 padding.
func DecryptSessionKey(priv, peer, ciphertext []byte) ([]byte, error) {
	k, err := AuthorizationAESKey(priv, peer)
	if err != nil {
		return nil, err
	}
	return decryptCBC(k, ciphertext)
}

// DecryptSessionKeyLegacyUnpadded is the historical unpadded-X candidate.
// Callers should only select it after validating a real authorization response.
func DecryptSessionKeyLegacyUnpadded(priv, peer, ciphertext []byte) ([]byte, error) {
	k, err := AuthorizationAESKeyLegacyUnpadded(priv, peer)
	if err != nil {
		return nil, err
	}
	return decryptCBC(k, ciphertext)
}

func decryptCBC(k, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, ErrMalformed
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ciphertext))
	iv := append([]byte(nil), k...)
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		b.Decrypt(out[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
		for j := 0; j < aes.BlockSize; j++ {
			out[i+j] ^= iv[j]
		}
		copy(iv, ciphertext[i:i+aes.BlockSize])
	}
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(out) {
		return nil, ErrInvalidPadding
	}
	for _, v := range out[len(out)-pad:] {
		if int(v) != pad {
			return nil, ErrInvalidPadding
		}
	}
	return out[:len(out)-pad], nil
}
