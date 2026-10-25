package deviceprofile

import "testing"

func TestAuthIdentityUsesDifferentSaltAndDoubleHex(t *testing.T) {
	const loginID = "490102030405060708090a0b0c0d0e0f"
	a, b := AuthIdentityPair(loginID)
	// Python hashlib plus bytes.hex() fixture, recovered independently from
	// the baseinfo helper's own call-site salts and %x formatting.
	if a != "3137643961306538-64396130-39613065-37643961-373363323337343633323630" || b != "3936653430396263-65343039-34303962-36653430-333662653038643161653661" {
		t.Fatal("authorization identity differs from independent fixture")
	}
	c, d := IdentityPair(loginID)
	if a == c || b == d || len(a) != 68 || len(c) != 36 {
		t.Fatal("different native identity functions conflated")
	}
	want := "<softtype><k3>18.4.1</k3><k9>iPad</k9><k10>6</k10><k19>" + a + "</k19><k20>" + b + "</k20><k22>(null)</k22><k33>微信</k33><k47>1</k47><k50>1</k50><k51>com.tencent.xin</k51><k54>iPad15,6</k54><k61>2</k61></softtype>"
	got := AuthSoftType(loginID, OSVersion, Model)
	if got != want || len(got) != 334 {
		t.Fatal("authorization XML changed")
	}
}
