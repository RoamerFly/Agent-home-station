package authwire

import "unicode/utf8"

// ExtInfoV8Parts contains outputs of the version-8 device-data producers.
// This codec does not produce device attestations or mint a device token. Those
// producers remain under reconstruction. Callers must supply their real output.
// Timestamps are explicit because their producer's clock mapping is unverified.
type ExtInfoV8Parts struct {
	WCSTF, WCSTE, EncryptedDeviceData []byte
	DeviceToken                       string
	CryptoTimestamp, TokenTimestamp   uint32
}

// BuildExtInfoV8 recovers the container populated by the pinned SecManualAuth:
// CryptoData and DeviceToken -> WCExtInfo fields 1,2,3,7 -> AES field 24.
// The byte encoding is independently checked against the recovered descriptor;
// it does not establish that the supplied proof is accepted by WeChat.
func BuildExtInfoV8(p ExtInfoV8Parts) ([]byte, error) {
	if len(p.WCSTF) == 0 || len(p.WCSTE) == 0 || len(p.EncryptedDeviceData) == 0 || len(p.WCSTF) > maxPayload/4 || len(p.WCSTE) > maxPayload/4 || len(p.EncryptedDeviceData) > maxPayload/4 || p.DeviceToken == "" || len(p.DeviceToken) > 16384 || !utf8.ValidString(p.DeviceToken) {
		return nil, ErrMissing
	}
	crypto := ab(nil, 1, []byte("00000008"))
	crypto = av(crypto, 2, 1)
	crypto = ab(crypto, 3, p.EncryptedDeviceData)
	crypto = av(crypto, 4, uint64(p.CryptoTimestamp))
	crypto = av(crypto, 5, 5)
	crypto = av(crypto, 6, 0) // present zero-valued uint32 pointer in the ELF

	token := ab(nil, 1, nil) // present empty string pointer
	token = av(token, 2, 1)
	token = ab(token, 3, ab(nil, 1, []byte(p.DeviceToken)))
	token = av(token, 4, uint64(p.TokenTimestamp))
	token = av(token, 5, 2)
	token = av(token, 6, 0)

	buffer := func(data []byte) []byte {
		return marshalBuffer(Buffer{Length: p32(uint32(len(data))), Data: data, HasData: true})
	}
	out := ab(nil, 1, buffer(p.WCSTF))
	out = ab(out, 2, buffer(p.WCSTE))
	out = ab(out, 3, buffer(crypto))
	return ab(out, 7, buffer(token)), nil
}
