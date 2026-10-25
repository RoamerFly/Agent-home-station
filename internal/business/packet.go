// Package business reconstructs the ordinary (CGI 522/139, type 5, uncompressed)
// WeChat business envelope. Constants and layout are protocol observations and
// historical reference candidates; live interoperability is not yet established.
package business

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

const TextCGI = 522
const InitCGI = 139
const MaxPayload = 2 << 20

var ErrPacket = errors.New("invalid or unsupported business packet")

type Session struct {
	ClientVersion uint32
	UIN           uint32
	SessionKey    []byte
	Cookie        []byte
	LoginSalt     []byte
}

type Signer func(ciphertext []byte) (uint32, error)

func (s Session) Validate() error {
	if s.ClientVersion == 0 || s.UIN == 0 || len(s.SessionKey) != 16 || len(s.Cookie) != 15 || len(s.LoginSalt) == 0 || len(s.LoginSalt) > 4096 {
		return errors.New("incomplete or unsupported business session")
	}
	return nil
}

func EncodeText(payload []byte, s Session, sign Signer) ([]byte, error) {
	return EncodeOperation(payload, s, TextCGI, sign)
}

// EncodeOperation supports only operations whose static SendPostData templates
// have been checked: newsendmsg CGI522 and newinit CGI139, encryption type5.
// It does not guess the framing for an arbitrary CGI number.
func EncodeOperation(payload []byte, s Session, cgi uint32, sign Signer) ([]byte, error) {
	if cgi != TextCGI && cgi != InitCGI {
		return nil, ErrPacket
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if len(payload) == 0 || len(payload) > MaxPayload || sign == nil {
		return nil, ErrPacket
	}
	block, err := aes.NewCipher(s.SessionKey)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(payload)%aes.BlockSize
	plain := append(append([]byte(nil), payload...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	body := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, s.SessionKey).CryptBlocks(body, plain)
	rqt, err := sign(body)
	if err != nil {
		return nil, err
	}
	uin := binary.BigEndian.AppendUint32(nil, s.UIN)
	inner := md5.Sum(append(uin, s.LoginSalt...))
	seed := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	seed = append(seed, s.LoginSalt...)
	seed = append(seed, inner[:]...)
	digest := md5.Sum(seed)
	checksum := crc32.ChecksumIEEE(append(digest[:], payload...))
	header := []byte{0xbf, 0, 0x5f}
	header = binary.BigEndian.AppendUint32(header, s.ClientVersion)
	header = binary.BigEndian.AppendUint32(header, s.UIN)
	header = append(header, s.Cookie...)
	for _, n := range []uint64{uint64(cgi), uint64(len(payload)), uint64(len(payload))} {
		header = binary.AppendUvarint(header, n)
	}
	header = append(header, 0, 0x0d)
	header = binary.AppendUvarint(header, uint64(checksum))
	header = append(header, 0xff)
	header = binary.AppendUvarint(header, uint64(rqt))
	header = append(header, 0)
	if len(header) > 63 {
		return nil, ErrPacket
	}
	header[1] = byte(len(header)<<2) | 2
	return append(header, body...), nil
}

// Decode verifies bounds and padding before returning plaintext. The outer
// transport must authenticate MMTLS first; this CBC format is not authenticated.
func Decode(packet []byte, s Session) (payload, cookie []byte, err error) {
	if err = s.Validate(); err != nil {
		return nil, nil, err
	}
	if len(packet) < 12 || len(packet) > MaxPayload+4096 || packet[0] != 0xbf {
		return nil, nil, ErrPacket
	}
	headerLen := int(packet[1] >> 2)
	mode := packet[1] & 3
	cookieLen := int(packet[2] & 15)
	if packet[2]>>4 != 5 || (mode != 1 && mode != 2) || headerLen < 11+cookieLen || headerLen >= len(packet) {
		return nil, nil, ErrPacket
	}
	if binary.BigEndian.Uint32(packet[7:11]) != s.UIN {
		return nil, nil, ErrPacket
	}
	cursor := 11 + cookieLen
	if cursor > headerLen {
		return nil, nil, ErrPacket
	}
	_, n := binary.Uvarint(packet[cursor:headerLen])
	if n <= 0 {
		return nil, nil, ErrPacket
	}
	cursor += n
	plainLen, n := binary.Uvarint(packet[cursor:headerLen])
	if n <= 0 || plainLen > MaxPayload {
		return nil, nil, ErrPacket
	}
	body := packet[headerLen:]
	if len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return nil, nil, ErrPacket
	}
	block, _ := aes.NewCipher(s.SessionKey)
	plain := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, s.SessionKey).CryptBlocks(plain, body)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(plain) {
		return nil, nil, ErrPacket
	}
	for _, v := range plain[len(plain)-pad:] {
		if int(v) != pad {
			return nil, nil, ErrPacket
		}
	}
	plain = plain[:len(plain)-pad]
	if mode == 1 {
		reader, e := zlib.NewReader(bytes.NewReader(plain))
		if e != nil {
			return nil, nil, ErrPacket
		}
		plain, e = io.ReadAll(io.LimitReader(reader, MaxPayload+1))
		closeErr := reader.Close()
		if e != nil || closeErr != nil || len(plain) > MaxPayload {
			return nil, nil, ErrPacket
		}
	}
	if uint64(len(plain)) != plainLen {
		return nil, nil, ErrPacket
	}
	return plain, append([]byte(nil), packet[11:11+cookieLen]...), nil
}
