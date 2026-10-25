package deviceprofile

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
)

// AuthIdentityPair is baseinfo.IOSUuid@7c7860. It intentionally differs from
// IdentityPair@7c0a00: shorter salts and a second hex encoding of digest text.
func AuthIdentityPair(input string) (string, string) {
	encode := func(salt string) string {
		sum := md5.Sum([]byte(input + salt))
		s := hex.EncodeToString(sum[:])
		return fmt.Sprintf("%x-%x-%x-%x-%x", s[:8], s[2:6], s[3:7], s[1:5], s[20:])
	}
	return encode("SM202003220432"), encode("BM202003220432")
}

func AuthSoftType(loginDeviceID, osVersion, model string) string {
	a, b := AuthIdentityPair(loginDeviceID)
	return "<softtype><k3>" + osVersion + "</k3><k9>iPad</k9><k10>6</k10><k19>" + a + "</k19><k20>" + b + "</k20><k22>(null)</k22><k33>微信</k33><k47>1</k47><k50>1</k50><k51>com.tencent.xin</k51><k54>" + model + "</k54><k61>2</k61></softtype>"
}
