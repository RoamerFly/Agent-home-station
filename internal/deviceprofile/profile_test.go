package deviceprofile

import (
	"math"
	"strings"
	"testing"
)

func completeSyntheticProfile() Profile {
	return Profile{Schema: Schema, DeviceID: "synthetic-device", IDFV: "synthetic-idfv", IDFA: "synthetic-idfa", SDI: "synthetic-sdi", DeviceName: "synthetic-name", Model: "synthetic-model", OSVersion: "synthetic-os", DeviceType: "iPad Air iPadOS18.4.1", ClientVersion: 402668320, CoreCount: 3, InstallTime: 123, BootTime: 456, SystemVersion: &FileStat{Inode: math.MaxUint64, Changed: Timespec{Seconds: 789}}, DyldCache: &FileStat{}, Var: &FileStat{}, EtcGroup: &FileStat{}, EtcHosts: &FileStat{}}
}

func TestFingerprintAttributeOrderAndPrecision(t *testing.T) {
	a, err := completeSyntheticProfile().FingerprintAttributes()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields("deviceid sdi idfv idfa device_model os_version core_count carrier_name is_jailbreak device_name client_version plist_version language locale_country screen_width screen_height install_time kern_boottime ft_sysverplist_inode ft_sysverplist_at ft_sysverplist_mt ft_sysverplist_ct ft_sysverplist_bt ft_var_inode ft_var_at ft_var_mt ft_var_ct ft_var_bt ft_etcgroup_inode ft_etcgroup_at ft_etcgroup_mt ft_etcgroup_ct ft_etcgroup_bt ft_etchosts_inode ft_etchosts_at ft_etchosts_mt ft_etchosts_ct ft_etchosts_bt ft_dyldcache_inode ft_dyldcache_at ft_dyldcache_mt ft_dyldcache_ct ft_dyldcache_bt")
	if len(a) != 43 || len(a) != len(want) {
		t.Fatal("wrong attribute count")
	}
	for i := range want {
		if a[i].Key != want[i] {
			t.Fatalf("attribute %d: %s", i, a[i].Key)
		}
	}
	if a[18].Value != "18446744073709551615" || a[21].Value != "789" || a[7].Value != "" || a[12].Value != "zh" || a[13].Value != "CN" || a[14].Value != "810" || a[15].Value != "1080" {
		t.Fatal("attribute values lost precision or profile branch")
	}
}

func TestIncompleteProfileDoesNotBecomeFingerprint(t *testing.T) {
	p := completeSyntheticProfile()
	p.SystemVersion = nil
	if _, err := p.FingerprintAttributes(); err == nil {
		t.Fatal("missing stats silently substituted")
	}
	p = completeSyntheticProfile()
	p.DeviceType = "unknown device"
	if _, err := p.FingerprintAttributes(); err == nil {
		t.Fatal("unknown profile accepted")
	}
	p = completeSyntheticProfile()
	p.Var.Access.Nanos = math.MaxInt64
	if _, err := p.FingerprintAttributes(); err != nil {
		t.Fatal("native full-width profile Nsec rejected")
	}
}
