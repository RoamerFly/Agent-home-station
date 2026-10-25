// Reconstructed Go formatting semantics versus independent Python static vectors.
// This is newly written source; it never invokes the original program or library.
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	raw, err := os.ReadFile("research/evidence/ipad-auth-identity-vectors.json")
	if err != nil {
		panic(err)
	}
	var fixture struct {
		Examples []struct {
			DeviceID string `json:"synthetic_login_device_id"`
			One      string `json:"auth_identity_one"`
			Two      string `json:"auth_identity_two"`
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		panic(err)
	}
	for _, example := range fixture.Examples {
		for index, salt := range []string{"SM202003220432", "BM202003220432"} {
			digest := md5.Sum([]byte(example.DeviceID + salt))
			s := hex.EncodeToString(digest[:])
			actual := fmt.Sprintf("%x-%x-%x-%x-%x", s[:8], s[2:6], s[3:7], s[1:5], s[20:32])
			want := example.One
			if index == 1 {
				want = example.Two
			}
			if actual != want || len(actual) != 68 {
				panic("static formula mismatch")
			}
		}
	}
	fmt.Printf("PASS: %d synthetic identities; 14-byte salts and %%x of string slices verified\n", len(fixture.Examples))
}
