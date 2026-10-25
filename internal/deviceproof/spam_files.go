package deviceproof

import (
	"errors"
	"hash/crc32"
	"strings"
	"unicode/utf8"

	"wechat-bot/internal/rqtx"
)

// SpamContext is the source GetNewSpamDataV8's uint32 Unix seconds expression.
// Overflow is intentional and follows the original unsigned machine operations.
func SpamContext(unixSeconds uint32) uint32 { return unixSeconds*0xffffffed + 7 }

// SpamDilate32 maps source bit i to result bit 2*i. The selected client applies
// this to core count, checksums, and rqtx outputs, not a generic XOR operation.
func SpamDilate32(input uint32) uint64 {
	var result uint64
	for i := uint(0); i < 32; i++ {
		result |= uint64((input>>i)&1) << (2 * i)
	}
	return result
}

// SpamXORString preserves the selected Go implementation's per-byte XOR followed
// by string(rune). High bytes thus expand to two UTF-8 bytes; raw byte XOR is not
// equivalent. This function also handles arbitrary input UTF-8 byte sequences.
func SpamXORString(input string, context uint32) string {
	var result strings.Builder
	result.Grow(len(input) * 2)
	for i := 0; i < len(input); i++ {
		result.WriteRune(rune(input[i] ^ byte(context)))
	}
	return result.String()
}

// GetFileInfo reconstructs the selected profile's fifteen fixed app images and
// their build UUIDs. GUID2 is the app bundle container UUID from DeviceInfo+138.
// These describe the selected virtual client profile, not files on this host.
func GetFileInfo(guid2 string, context uint32) ([]*SpamFileInfo, error) {
	if !spamUUID(guid2) {
		return nil, errors.New("deviceproof: GUID2 must be a canonical UUID")
	}
	result := make([]*SpamFileInfo, 0, len(spamFiles))
	for _, v := range spamFiles {
		path := SpamXORString("/var/containers/Bundle/Application/"+guid2+v.suffix, context)
		uuid := SpamXORString(v.uuid, context)
		result = append(result, &SpamFileInfo{Filepath: &path, Fileuuid: &uuid})
	}
	return result, nil
}

// SpamFileInfoCRC follows the separate plaintext path list at b42cd3..b434b0.
// Its prefix contains /private, although GetFileInfo's encoded paths do not.
// The list is path, UUID, path, UUID, joined without delimiters, then CRC32 IEEE
// and bit dilation. The caller formats the result in decimal (fmt "%v" with an
// int64 argument in the binary).
func SpamFileInfoCRC(guid2 string) (uint64, error) {
	if !spamUUID(guid2) {
		return 0, errors.New("deviceproof: GUID2 must be a canonical UUID")
	}
	h := crc32.NewIEEE()
	for _, v := range spamFiles {
		_, _ = h.Write([]byte("/private/var/containers/Bundle/Application/" + guid2 + v.suffix))
		_, _ = h.Write([]byte(v.uuid))
	}
	return SpamDilate32(h.Sum32()), nil
}

// SpamRQTHash is the source wrapper's direct rqtx(SDI), followed by bit dilation.
// The independently verified rqtx VM accepts exactly 32 lowercase hex characters;
// this entry point deliberately retains that narrower verified input contract.
func SpamRQTHash(sdi string) (uint64, error) {
	v, err := rqtx.SumHex(sdi)
	if err != nil {
		return 0, err
	}
	return SpamDilate32(v), nil
}

func spamUUID(s string) bool {
	if len(s) != 36 || !utf8.ValidString(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return false
			}
		} else if !((s[i] >= '0' && s[i] <= '9') || (s[i] >= 'a' && s[i] <= 'f') || (s[i] >= 'A' && s[i] <= 'F')) {
			return false
		}
	}
	return true
}
