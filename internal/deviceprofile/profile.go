// Package deviceprofile holds the persistent client profile shared by the
// fingerprint and authorization encoders. It does not mint server-issued tokens.
package deviceprofile

import (
	"errors"
	"strconv"
	"unicode/utf8"

	"wechat-bot/internal/loginwire"
)

const Schema = "wechat-ipad-v8-profile-1"

type Timespec struct{ Seconds, Nanos uint64 }
type FileStat struct {
	Inode                            uint64
	Access, Modified, Changed, Birth Timespec
}
type FileSystem struct {
	Type, Flags, FSID            uint64
	TypeName, MountOn, MountFrom string
}

// Profile stores a client identity, including the sample's virtual file stats.
// Generation is separate so a saved profile is never silently regenerated.
// Pointer fields distinguish missing stat input from an explicit zero value.
type Profile struct {
	Schema                                                                                  string
	UUIDOne, LoginDeviceID                                                                  string
	DeviceID, IDFV, IDFA, SDI                                                               string
	GUID1, GUID2, IMEI, DeviceMAC, Language, RealCountry, Brand, BundleID, OSType, AdSource string
	DeviceName, Model, OSVersion, DeviceType, CarrierName                                   string
	ClientVersion, CoreCount                                                                uint32
	InstallTime, BootTime                                                                   uint64
	SystemVersion, DyldCache, Var, EtcGroup, EtcHosts                                       *FileStat
	Filesystem                                                                              *FileSystem
}

func (p Profile) ValidateFingerprint() error {
	if p.Schema != Schema || p.ClientVersion == 0 || p.CoreCount == 0 || p.InstallTime == 0 || p.BootTime == 0 || p.SystemVersion == nil || p.DyldCache == nil || p.Var == nil || p.EtcGroup == nil || p.EtcHosts == nil {
		return errors.New("incomplete persistent fingerprint profile")
	}
	for _, s := range []string{p.DeviceID, p.IDFV, p.IDFA, p.SDI, p.DeviceName, p.Model, p.OSVersion, p.DeviceType} {
		if s == "" || len(s) > 4096 || !utf8.ValidString(s) {
			return errors.New("invalid persistent fingerprint text")
		}
	}
	if !utf8.ValidString(p.CarrierName) || len(p.CarrierName) > 4096 {
		return errors.New("invalid carrier text")
	}
	// These are protocol profile fields, not OS timespec values. The sample's
	// GenVar deliberately stores full rand.Int63 outputs in Nsec; normalizing
	// or rejecting them as >=1e9 would change the recovered wire data.
	return nil
}

// FingerprintAttributes reproduces the 43-record iPad request order recovered
// from SendIosDeviceTokenRequest. The fixed 810x1080 branch belongs to the
// current iPad profile. Other device types require their own explicit profile.
func (p Profile) FingerprintAttributes() ([]loginwire.DeviceAttribute, error) {
	if err := p.ValidateFingerprint(); err != nil {
		return nil, err
	}
	if p.DeviceType != "iPad Air iPadOS18.4.1" {
		return nil, errors.New("unsupported fingerprint device profile")
	}
	u := func(v uint64) string { return strconv.FormatUint(v, 10) }
	version := u(uint64(p.ClientVersion))
	out := []loginwire.DeviceAttribute{
		{Key: "deviceid", Value: p.DeviceID}, {Key: "sdi", Value: p.SDI}, {Key: "idfv", Value: p.IDFV}, {Key: "idfa", Value: p.IDFA},
		{Key: "device_model", Value: p.Model}, {Key: "os_version", Value: p.OSVersion}, {Key: "core_count", Value: u(uint64(p.CoreCount))},
		{Key: "carrier_name", Value: p.CarrierName}, {Key: "is_jailbreak", Value: "0"}, {Key: "device_name", Value: p.DeviceName},
		{Key: "client_version", Value: version}, {Key: "plist_version", Value: version}, {Key: "language", Value: "zh"}, {Key: "locale_country", Value: "CN"},
		{Key: "screen_width", Value: "810"}, {Key: "screen_height", Value: "1080"}, {Key: "install_time", Value: u(p.InstallTime)}, {Key: "kern_boottime", Value: u(p.BootTime)},
	}
	for _, entry := range []struct {
		prefix string
		stat   *FileStat
	}{{"ft_sysverplist", p.SystemVersion}, {"ft_var", p.Var}, {"ft_etcgroup", p.EtcGroup}, {"ft_etchosts", p.EtcHosts}, {"ft_dyldcache", p.DyldCache}} {
		for _, v := range []struct {
			suffix string
			value  uint64
		}{{"inode", entry.stat.Inode}, {"at", entry.stat.Access.Seconds}, {"mt", entry.stat.Modified.Seconds}, {"ct", entry.stat.Changed.Seconds}, {"bt", entry.stat.Birth.Seconds}} {
			out = append(out, loginwire.DeviceAttribute{Key: entry.prefix + "_" + v.suffix, Value: u(v.value)})
		}
	}
	return out, nil
}
