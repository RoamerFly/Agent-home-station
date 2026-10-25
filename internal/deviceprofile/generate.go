package deviceprofile

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"
)

const ClientVersion uint32 = 402668320
const DeviceType = "iPad Air iPadOS18.4.1"
const DeviceName = "iPad Air (第7代)"
const Model = "iPad15,6"
const OSVersion = "18.4.1"

// IdentityPair reproduces baseutils.IOSUuid, including its unusual overlapping
// digest slices. The input is LoginData.Deviceid_str, independently persisted
// from DeviceInfo.DeviceID. It is not a service-issued device token.
func IdentityPair(input string) (string, string) {
	encode := func(salt string) string {
		sum := md5.Sum([]byte(input + salt))
		s := hex.EncodeToString(sum[:])
		return s[:8] + "-" + s[2:6] + "-" + s[3:7] + "-" + s[1:5] + "-" + s[20:]
	}
	return encode("SM2020032204320"), encode("BM2020032204321")
}

type GenerateOptions struct {
	// LoginDeviceID is the existing textual login identity. Generating the
	// independent profile must never replace an already established QR identity.
	LoginDeviceID string
	Now           time.Time
	Location      *time.Location
	UUIDEntropy   io.Reader
	Random        *rand.Rand
}

// Generate reconstructs the pinned iPad constructor's distribution and field
// rules. Random draws are explicit caller inputs: it does not reproduce the
// original process's global rand.Seed timing or its unknown UUID reader.
// The returned profile must be saved once and reused by all login stages.
func Generate(opt GenerateOptions) (Profile, error) {
	var p Profile
	if opt.LoginDeviceID == "" || len(opt.LoginDeviceID) > 256 || !utf8.ValidString(opt.LoginDeviceID) || opt.Now.IsZero() || opt.Location == nil || opt.UUIDEntropy == nil || opt.Random == nil {
		return p, errors.New("explicit identity, clock, location and entropy are required")
	}
	uuid := func() (string, error) {
		var b [16]byte
		if _, err := io.ReadFull(opt.UUIDEntropy, b[:]); err != nil {
			return "", err
		}
		b[6] = b[6]&15 | 0x40
		b[8] = b[8]&63 | 0x80
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
	}
	uuidOne, err := uuid()
	if err != nil {
		return p, err
	}
	guid1, err := uuid()
	if err != nil {
		return p, err
	}
	guid2, err := uuid()
	if err != nil {
		return p, err
	}
	sdiUUID, err := uuid()
	if err != nil {
		return p, err
	}
	md5Text := func(s string) string { sum := md5.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }
	idfv, idfa := IdentityPair(opt.LoginDeviceID)
	p = Profile{Schema: Schema, UUIDOne: uuidOne, LoginDeviceID: opt.LoginDeviceID, DeviceID: md5Text(strings.ReplaceAll(uuidOne, "-", "")), IDFV: idfv, IDFA: idfa, SDI: md5Text(sdiUUID), GUID1: guid1, GUID2: guid2, DeviceName: DeviceName, Model: Model, OSVersion: OSVersion, DeviceType: DeviceType, CarrierName: "中国电信", ClientVersion: ClientVersion, CoreCount: 3, Language: "zh_CN", RealCountry: "CN", Brand: "Apple", BundleID: "com.tencent.xin", OSType: DeviceType}
	r := opt.Random
	sampleDate := func(start, end time.Time) time.Time { return start.Add(time.Duration(r.Int63n(int64(end.Sub(start))))) }
	p.InstallTime = uint64(sampleDate(time.Date(2025, 4, 30, 0, 0, 0, 0, opt.Location), time.Date(2025, 5, 30, 23, 59, 59, 0, opt.Location)).Unix())
	p.BootTime = uint64(opt.Now.In(opt.Location).AddDate(0, 0, -7).Add(time.Duration(r.Intn(86401)) * time.Second).Unix())
	t := uint64(sampleDate(time.Date(2025, 4, 20, 0, 0, 0, 0, opt.Location), time.Date(2025, 5, 10, 23, 59, 59, 0, opt.Location)).Unix())
	stat := func() *FileStat {
		stamp := Timespec{Seconds: t}
		return &FileStat{Inode: uint64(r.Int63()), Access: stamp, Modified: stamp, Changed: stamp, Birth: stamp}
	}
	p.SystemVersion = stat()
	p.DyldCache = stat()
	varTime := uint64(opt.Now.In(opt.Location).AddDate(-1, 0, 0).AddDate(0, 0, r.Intn(30)+1).Unix())
	access := Timespec{Seconds: t - 2592000, Nanos: uint64(r.Int63())}
	modified := Timespec{Seconds: varTime, Nanos: uint64(r.Int63())}
	p.Var = &FileStat{Inode: 2, Access: access, Birth: access, Modified: modified, Changed: modified}
	p.EtcGroup = stat()
	p.EtcHosts = stat()
	const alphabet = "0123456789abcdef"
	suffix := make([]byte, 40)
	for i := range suffix {
		suffix[i] = alphabet[r.Intn(len(alphabet))]
	}
	p.Filesystem = &FileSystem{Type: 0x1a, TypeName: "apfs", Flags: 0x5480d009, MountOn: "/", MountFrom: "com.apple.os.update-{" + string(suffix) + "}@/dev/disk0s1s1", FSID: 0x1a32000001}
	return p, p.ValidateFingerprint()
}
