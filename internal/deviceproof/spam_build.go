package deviceproof

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math/bits"
	"strconv"
	"time"

	"wechat-bot/internal/deviceprofile"
	"wechat-bot/internal/v08"
)

// SpamBuild retains the selected executable's app and kernel build constants.
// It is separate from the runtime profile: silently normalizing inconsistent
// profile/version strings would change the observed source data flow.
type SpamBuild struct{ HeadMD5, IOSBuildVersion, KernelType, KernelVersion, KernelRelease string }

func SelectedSpamBuild() SpamBuild {
	return SpamBuild{
		HeadMD5: "d05a80a94b6c2e3c31424403437b6e18", IOSBuildVersion: "19H386", KernelType: "Darwin", KernelVersion: "21.6.0",
		KernelRelease: "Darwin Kernel Version 21.6.0: Sun Oct 15 00:17:39 PDT 2023; root:xnu-8020.241.42~8/RELEASE_ARM64_T7000",
	}
}

// SpamInput exposes source objects that have distinct meanings in the selected
// binary. IDFV/IDFA must already be the verified IOSUuid(LoginDeviceID) pair.
// ServerDeviceToken must come from the server response; this package never mints
// it. The wire builder cannot establish the provenance of caller-supplied data.
type SpamInput struct {
	Profile                                 deviceprofile.Profile
	LoginDeviceID, DeviceInfoName, RomModel string
	StartTime, NowTime                      time.Time
	ServerDeviceToken                       string
	Build                                   SpamBuild
}

func spamPtr[T any](v T) *T { return &v }

// BuildSpamBody reconstructs the complete selected GetNewSpamDataV8 body. It
// requires explicit client profile, token, build and time inputs and performs
// neither network requests nor local environment fabrication.
func BuildSpamBody(in SpamInput) (*SpamBody, error) {
	p := in.Profile
	if err := p.ValidateFingerprint(); err != nil {
		return nil, err
	}
	if p.Filesystem == nil || !spamUUID(p.GUID1) || !spamUUID(p.GUID2) || in.LoginDeviceID == "" || in.DeviceInfoName == "" || in.RomModel == "" || in.ServerDeviceToken == "" || in.StartTime.IsZero() || in.NowTime.IsZero() {
		return nil, errors.New("deviceproof: incomplete spam input")
	}
	for _, s := range []string{in.LoginDeviceID, in.DeviceInfoName, in.RomModel, in.ServerDeviceToken, in.Build.HeadMD5, in.Build.IOSBuildVersion, in.Build.KernelType, in.Build.KernelVersion, in.Build.KernelRelease} {
		if len(s) == 0 || len(s) > 4096 {
			return nil, errors.New("deviceproof: invalid spam input length")
		}
	}
	if in.StartTime.Unix() < 0 || in.StartTime.Unix() > 0xffffffff || in.NowTime.Unix() < 0 || in.NowTime.Unix() > 0xffffffff {
		return nil, errors.New("deviceproof: spam time outside uint32 epoch")
	}
	idfv, idfa := deviceprofile.IdentityPair(in.LoginDeviceID)
	if p.IDFV != idfv || p.IDFA != idfa {
		return nil, errors.New("deviceproof: spam identity pair differs from LoginDeviceID")
	}
	seconds := uint32(in.StartTime.Unix())
	xor := SpamContext(seconds)
	x := func(s string) *string { return spamPtr(SpamXORString(s, xor)) }
	u := func(v uint64, k uint32) *uint64 { return spamPtr(v08.EncodeUInt64(v, k, seconds)) }
	s := func(v string, k uint32) *string { return spamPtr(string(v08.EncodeString([]byte(v), k, seconds))) }
	files, err := GetFileInfo(p.GUID2, xor)
	if err != nil {
		return nil, err
	}
	rqt, err := SpamRQTHash(p.SDI)
	if err != nil {
		return nil, err
	}
	fileCRC, err := SpamFileInfoCRC(p.GUID2)
	if err != nil {
		return nil, err
	}
	version := int64(SpamDilate32(p.ClientVersion))
	b := &SpamBody{
		UnKnown1: spamPtr(int32(1)), TimeStamp: &seconds, KeyHash: spamPtr(int32(SpamDilate32(uint32(byte(xor))))),
		Yes1: x("yes"), Yes2: x("yes"), IosVersion: x(p.OSVersion), DeviceType: x(in.DeviceInfoName), CoreCount: spamPtr(int64(SpamDilate32(p.CoreCount))),
		IdentifierForVendor: x(p.IDFV), AdvertisingIdentifier: x(p.IDFA), Carrier: x(p.CarrierName), BatteryInfo: spamPtr(int32(1)), NetworkName: []string{SpamXORString("en0", xor), SpamXORString("utun3", xor)}, NetType: spamPtr(int32(1)),
		AppBundleId: x("com.tencent.xin"), DeviceName: x(p.DeviceName), UserName: x(in.RomModel), GetVersion: &version, GetVersionFromPList: spamPtr(version), Unknown5: spamPtr(int32(0)), Unknown6: spamPtr(int32(4)),
		Lang: x("zh"), Country: x("CN"), Unknown7: spamPtr(int32(4)), DocumentDir: x("/var/mobile/Containers/Data/Application/" + p.GUID1 + "/Documents"), Unknown8: spamPtr(int32(0)), Unknown9: spamPtr(int32(0)),
		HeadMD5: x(in.Build.HeadMD5), AppUUID: x(p.IDFV), SyslogUUID: spamPtr(""), Unknown10: spamPtr(""), Unknown11: spamPtr(""), AppName: x("微信"), SshPath: x("/usr/bin/ssh"), TempTest: x("/tmp/test.txt"), DevMD5: spamPtr(""), DevUser: spamPtr(""), DevPrefix: spamPtr(""),
		AppFileInfo: files, Unknown12: spamPtr(""), IsModify: spamPtr(int32(0)), Sdi: x(p.SDI), RqtHash: spamPtr(int64(rqt)), InstallTime: spamPtr(p.InstallTime), KernBootTime: spamPtr(p.BootTime), Unknown55: spamPtr(uint64(0)), RqtHash56: spamPtr(int64(rqt)), Unknown57: spamPtr(uint64(0)),
		DeviceId: x(p.DeviceID), DeviceIdCrc: spamPtr(int64(SpamDilate32(crc32.ChecksumIEEE([]byte(p.DeviceID))))), AppFileInfoCrc: spamPtr(strconv.FormatUint(fileCRC, 10)), Unknown61: x("2FFC7F6DFEEFFF2B3FFCA029"), Unknown62: spamPtr(uint64(0x1051151051115115)),
		Si: spamPtr(""), DeviceToken: x(in.ServerDeviceToken),
		NowUsec:            spamPtr(uint32(in.StartTime.Nanosecond() / 1000)),
		SystemVersionCtime: u(p.SystemVersion.Changed.Seconds*1_000_000_000, 0xd91ca739), DyldSharedCacheArm64Ctime: u(p.DyldCache.Changed.Seconds*1_000_000_000, 0xa3071842), PrivateVarDirCTime: u(p.Var.Changed.Seconds*1_000_000_000, 0x5a5f1852), EmptyString: spamPtr(""), IsCaptured: spamPtr(uint64(0)),
		FixedOne: u(1, 0x75a9f3fa), PrivateVarDir: &SpamFileStat{StBirthtime: u(p.Var.Birth.Seconds*1_000_000_000+p.Var.Birth.Nanos, 0x2d6aca52), StCtime: u(p.Var.Changed.Seconds*1_000_000_000+p.Var.Changed.Nanos, 0x2d6aca52), StMtime: u(p.Var.Modified.Seconds*1_000_000_000+p.Var.Modified.Nanos, 0x2d6aca52)},
		WechatDocBirth: u(p.InstallTime*1_000_000_000+0x12241352, 0x30c0d228), LAPolicyDeviceOwnerAuthentication: u(1, 0xa0dac236), LAPolicyDeviceOwnerAuthenticationWithBiometrics: u(1, 0x6c964322), ICloudLogin: u(1, 0x92a52bce),
		AppStoreReceiptURL: s("/private/var/mobile/Containers/Data/Application/"+p.GUID1+"/StoreKit/Receipt", 0x985b22aa), SandboxReceiptExist: u(0, 0x236de3ff), NSClassFromString: u(0, 0x3aa9a3ce),
		IosBuildVersion: s(in.Build.IOSBuildVersion, 0x554738e7), KernelType: s(in.Build.KernelType, 0xec1b9627), DeviceModel: s(in.RomModel, 0x562e8bdc), KernelVersion: s(in.Build.KernelVersion, 0x318bc072), KernelRelease: s(in.Build.KernelRelease, 0x3b696b8d), DevciceType: s(p.Model, 0x554738e7),
	}
	usec := uint32(in.NowTime.Nanosecond() / 1000)
	shift := usec & 31
	nowSeconds := uint64(in.NowTime.Unix())
	now := uint32((nowSeconds<<shift)|(nowSeconds>>(32-shift))) ^ usec
	now = (now & 0xfffffc3f) | 0x40
	b.Now = &now
	b.NowUsecScale = spamPtr(*b.NowUsec*0x68323 + 0x11)
	f := p.Filesystem
	b.StatfsInfo = []*SpamStatFS{{FType: u(f.Type, 0x99687b8a), FFstypename: s(f.TypeName, 0xe2868e99), FFlags: u(f.Flags, 0xc36fa9a2), FMntonname: s(f.MountOn, 0xb62136b0), FMntfromname: s(f.MountFrom, 0x47ee23c4), FFsid: u(f.FSID, 0xa19a7ed5)}}
	var crcInput []byte
	for _, v := range []uint64{f.Flags, f.Type, f.FSID} {
		crcInput = binary.LittleEndian.AppendUint64(crcInput, v)
	}
	crcInput = append(crcInput, f.TypeName...)
	crcInput = append(crcInput, f.MountOn...)
	crcInput = append(crcInput, f.MountFrom...)
	crc := bits.RotateLeft32(crc32.ChecksumIEEE(crcInput), 1) ^ seconds ^ 0xc5b26fe3
	b.StatfsCrc = spamPtr(bits.RotateLeft32(crc, 1))
	for _, v := range spamAppex {
		b.AppexList = append(b.AppexList, *s(v, 0x540cf80d))
	}
	for _, v := range spamFrameworks {
		b.FrameworkList = append(b.FrameworkList, *s(v, 0x8c36ee2a))
	}
	if _, err := b.MarshalBinary(); err != nil {
		return nil, err
	}
	return b, nil
}

// MarshalSpamCheckData inserts the NewClientCheckData layer between the body and
// compression. Omitting it changes the observed b4510e..b45238 data flow.
func MarshalSpamCheckData(body *SpamBody, unixSeconds uint32) ([]byte, error) {
	raw, err := body.MarshalBinary()
	if err != nil {
		return nil, err
	}
	var out []byte
	out = spamInt(out, 1, uint64(crc32.ChecksumIEEE(raw)))
	out = spamInt(out, 2, uint64(unixSeconds))
	out = spamBytes(out, 3, raw)
	return out, nil
}
