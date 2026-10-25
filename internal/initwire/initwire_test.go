package initwire

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func u32p(v uint32) *uint32 { return &v }
func i32p(v int32) *int32   { return &v }
func sp(v string) *string   { return &v }
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestGoogleFixtureRequest(t *testing.T) {
	v := uint32(7)
	cv := int32(123)
	scene := uint32(3)
	n := uint32(2)
	got, err := MarshalRequest(NewInitRequest{BaseRequest: &BaseRequest{SessionKey: []byte("session"), HasSessionKey: true, UIN: &v, DeviceID: []byte("device"), HasDeviceID: true, ClientVersion: &cv, DeviceType: []byte("iPad"), HasDeviceType: true, Scene: &scene}, UserName: sp("wxid_fixture"), CurrentSynckey: &Buffer{Length: &n, Data: []byte("ck"), HasData: true}, MaxSynckey: &Buffer{Length: &n, Data: []byte("mk"), HasData: true}, Language: sp("zh_CN")})
	if err != nil {
		t.Fatal(err)
	}
	if want := fixture(t, "newinit-request.pb.hex"); string(got) != string(want) {
		t.Fatalf("Google protobuf fixture mismatch\n got %x\nwant %x", got, want)
	}
}
func TestGoogleFixtureResponseAndPresence(t *testing.T) {
	r, err := ParseResponse(fixture(t, "newinit-response.pb.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateForUse(); err != nil {
		t.Fatal(err)
	}
	if r.ContinueFlag == nil || *r.ContinueFlag != 1 || r.CmdCount == nil || *r.CmdCount != 1 || len(r.CmdList) != 1 || r.CmdList[0].ID == nil || *r.CmdList[0].ID != 5 || string(r.CmdList[0].Raw) != "raw" {
		t.Fatalf("lost optional or raw command data: %#v", r)
	}
}
func TestRejectsInconsistentCommandCount(t *testing.T) {
	b := fixture(t, "newinit-response.pb.hex") // field 6 (CmdCount=1) is changed to 2.
	for i := range b {
		if b[i] == 0x30 && i+1 < len(b) && b[i+1] == 1 {
			b[i+1] = 2
			break
		}
	}
	if _, err := ParseResponse(b); err == nil {
		t.Fatal("accepted mismatched CmdCount")
	}
}
func TestABIEvidenceMatchesSchema(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	p := filepath.Join(filepath.Dir(file), "..", "..", "research", "evidence", "newinit-go-layouts.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"name": "mm.NewInitRequest"`, `"name": "BaseRequest"`, `"tag": "protobuf:\"bytes,1,opt,name=BaseRequest`, `"tag": "protobuf:\"bytes,7,rep,name=CmdList`, `"name": "mm.NewInitResponse"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("ABI evidence lacks %s", want)
		}
	}
}
func TestNativeGateDoesNotTreatMissingRetAsSuccess(t *testing.T) {
	n := uint32(0)
	r := NewInitResponse{CurrentSynckey: &Buffer{Length: &n, HasData: true}, CmdList: []Command{{ID: i32p(1)}}}
	if r.ValidateNativeGate() != nil {
		t.Fatal("native structural gate unexpectedly failed")
	}
	if r.ValidateForUse() == nil {
		t.Fatal("application gate treated absent ret as success")
	}
}
