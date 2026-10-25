package deviceproof

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash/crc32"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/deviceprofile"
)

func TestSpamSchemaIndependentProtobuf(t *testing.T) {
	raw, err := os.ReadFile("testdata/spam_protobuf.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Vectors []struct {
			Type     string
			Input    json.RawMessage
			Expected string `json:"expected_hex"`
		}
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Vectors) != 4 {
		t.Fatal("missing independent protobuf vectors")
	}
	for _, v := range doc.Vectors {
		var target interface{ MarshalBinary() ([]byte, error) }
		switch v.Type {
		case "SpamFileInfo":
			target = &SpamFileInfo{}
		case "SpamFileStat":
			target = &SpamFileStat{}
		case "SpamStatFS":
			target = &SpamStatFS{}
		case "SpamBody":
			target = &SpamBody{}
		default:
			t.Fatal(v.Type)
		}
		if err = json.Unmarshal(v.Input, target); err != nil {
			t.Fatal(err)
		}
		got, err := target.MarshalBinary()
		if err != nil || hex.EncodeToString(got) != v.Expected {
			t.Fatalf("%s protobuf mismatch %x %v", v.Type, got, err)
		}
	}
}

func TestSpamFullBodyIndependentPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/spam_build.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Input    SpamInput
		Expected string `json:"expected_hex"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	body, err := BuildSpamBody(fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := body.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString(fixture.Expected)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		left, right := spamDecodeTest(t, got), spamDecodeTest(t, want)
		for tag, v := range left {
			w := right[tag]
			if v.number != w.number || !bytes.Equal(v.data, w.data) {
				t.Errorf("independent full-body field %d mismatch", tag)
			}
		}
		t.Fatalf("full-body mismatch: got %d, want %d bytes", len(got), len(want))
	}
}

func TestSpamScalarAndStringPrimitives(t *testing.T) {
	if SpamDilate32(0xffffffff) != 0x5555555555555555 || SpamDilate32(0x80000001) != 0x4000000000000001 || SpamDilate32(3) != 5 {
		t.Fatal("bit dilation changed")
	}
	if got := SpamXORString("\x00\x7f\x80\xff", 0x80); got != "\u0080\u00ff\x00\x7f" {
		t.Fatalf("rune expansion mismatch %x", got)
	}
	// Independent uint32 arithmetic: (1792807200 * -19 + 7) modulo 2^32.
	if got := SpamContext(1792807200); got != 0x11aabaa7 {
		t.Fatalf("context %08x", got)
	}
	files, err := GetFileInfo("01234567-89AB-CDEF-0123-456789ABCDEF", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 15 || !strings.HasSuffix(*files[0].Filepath, "/WeChat.app/WeChat") || *files[0].Fileuuid != "12623847-C8BD-3445-834D-2A01ED0D89DB" || !strings.HasSuffix(*files[14].Filepath, "/App.framework/App") {
		t.Fatal("fixed image list changed")
	}
	if _, err = GetFileInfo("../arbitrary-path", 1); err == nil {
		t.Fatal("invalid UUID accepted")
	}
	crc, err := SpamFileInfoCRC("01234567-89AB-CDEF-0123-456789ABCDEF")
	if err != nil {
		t.Fatal(err)
	}
	// Independent Python zlib.crc32 over the literal path/UUID sequence, dilated.
	if crc != 0x1111055050041454 {
		t.Fatalf("file-list CRC %016x", crc)
	}
}

func spamTestInput(t *testing.T) SpamInput {
	t.Helper()
	start := time.Date(2026, 10, 24, 10, 0, 0, 123456789, time.FixedZone("Asia/Shanghai", 8*3600))
	p, err := deviceprofile.Generate(deviceprofile.GenerateOptions{LoginDeviceID: "test-login-id", Now: start, Location: start.Location(), UUIDEntropy: bytes.NewReader(bytes.Repeat([]byte{0x36}, 64)), Random: rand.New(rand.NewSource(12345))})
	if err != nil {
		t.Fatal(err)
	}
	return SpamInput{Profile: p, LoginDeviceID: p.LoginDeviceID, DeviceInfoName: p.DeviceName, RomModel: p.Model, StartTime: start, NowTime: start.Add(time.Second + 1000*time.Nanosecond), ServerDeviceToken: "synthetic-server-token-offline-only", Build: SelectedSpamBuild()}
}

func TestCompleteSpamBodyAndCheckLayer(t *testing.T) {
	in := spamTestInput(t)
	b, err := BuildSpamBody(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.AppFileInfo) != 15 || len(b.AppexList) != 6 || len(b.FrameworkList) != 14 || len(b.StatfsInfo) != 1 {
		t.Fatal("lists incomplete")
	}
	if b.ShortBundleVersion != nil || b.UbiquityIdentityToken != nil {
		t.Fatal("unassigned optional fields unexpectedly present")
	}
	micro := uint32(123456)
	if b.NowUsec == nil || *b.NowUsec != micro || b.NowUsecScale == nil || *b.NowUsecScale != micro*0x68323+0x11 {
		t.Fatal("first clock microseconds changed")
	}
	if *b.UserName != SpamXORString(in.RomModel, SpamContext(uint32(in.StartTime.Unix()))) || *b.DeviceToken != SpamXORString(in.ServerDeviceToken, SpamContext(uint32(in.StartTime.Unix()))) {
		t.Fatal("source-object mapping changed")
	}
	raw, err := b.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := MarshalSpamCheckData(b, 1792807201)
	if err != nil {
		t.Fatal(err)
	}
	fields := spamDecodeTest(t, wrapper)
	if fields[1].number != uint64(crc32.ChecksumIEEE(raw)) || fields[2].number != 1792807201 || !bytes.Equal(fields[3].data, raw) {
		t.Fatal("missing NewClientCheckData layer")
	}
	bodyFields := spamDecodeTest(t, raw)
	if _, ok := bodyFields[1]; !ok {
		t.Fatal("required body fields absent")
	}
	if len(bodyFields) < 70 {
		t.Fatal("body unexpectedly sparse")
	}
	for _, change := range []func(*SpamInput){func(v *SpamInput) { v.ServerDeviceToken = "" }, func(v *SpamInput) { v.Profile.IDFV = "wrong" }, func(v *SpamInput) { v.Profile.Filesystem = nil }, func(v *SpamInput) { v.NowTime = time.Time{} }, func(v *SpamInput) { v.Profile.SDI = "not-digest" }} {
		bad := in
		change(&bad)
		if _, err := BuildSpamBody(bad); err == nil {
			t.Fatal("incomplete or inconsistent input accepted")
		}
	}
}

func TestSpamSchemaRejectsMissingRequiredAndOversized(t *testing.T) {
	if _, err := (&SpamBody{}).MarshalBinary(); err == nil {
		t.Fatal("missing required fields accepted")
	}
	if _, err := (&SpamFileStat{}).MarshalBinary(); err == nil {
		t.Fatal("missing required stat accepted")
	}
	if _, err := (&SpamFileInfo{Filepath: spamPtr(strings.Repeat("x", 65537))}).MarshalBinary(); err == nil {
		t.Fatal("oversized field accepted")
	}
	var nilBody *SpamBody
	if _, err := nilBody.MarshalBinary(); err == nil {
		t.Fatal("nil body accepted")
	}
	in := spamTestInput(t)
	body, err := BuildSpamBody(in)
	if err != nil {
		t.Fatal(err)
	}
	body.AppFileInfo = []*SpamFileInfo{nil}
	if _, err := body.MarshalBinary(); err == nil {
		t.Fatal("nil repeated message accepted")
	}
}

type spamTestField struct {
	number uint64
	data   []byte
}

func spamDecodeTest(t *testing.T, data []byte) map[uint64]spamTestField {
	t.Helper()
	out := make(map[uint64]spamTestField)
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 {
			t.Fatal("invalid wire key")
		}
		data = data[n:]
		f := spamTestField{}
		v, n := binary.Uvarint(data)
		if n <= 0 {
			t.Fatal("invalid wire value")
		}
		data = data[n:]
		if key&7 == 0 {
			f.number = v
		} else if key&7 == 2 && v <= uint64(len(data)) {
			f.data = data[:v]
			data = data[v:]
		} else {
			t.Fatal("invalid wire type/length")
		}
		out[key>>3] = f
	}
	return out
}
