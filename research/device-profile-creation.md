# `createDeviceInfo` and file-stat profile reconstruction

## Scope and evidence

This is an offline, static reconstruction of the supplied Go ELF.  It does
not execute the sample, contact a service, read any secret, or create a
device-token request.  Addresses below are ELF virtual addresses.  Function
names are from `research/evidence/main-functions.json`; the field names and
offsets are from the statically recovered Go ABI layouts in
`research/evidence/device-token-go-layouts.json`.

The entry point is `wechatdll/models/Login.createDeviceInfo` at `0xb9d700`.
It allocates `baseinfo.DeviceInfo` (type `0xf3e7c0`, size 416) and returns it.
The Go layout makes the writes below unambiguous.

## Directly recoverable `DeviceInfo` construction

| `DeviceInfo` field (offset) | Exact source / operation | Evidence |
|---|---|---|
| `UUIDOne` (`0x00`) | `baseutils.RandomUUID()` | `0xb9d732` |
| `UUIDTwo` (`0x10`) | left as the zero-value string by this constructor | no write between allocation and return |
| `Imei` (`0x20`) | left as the zero-value string | same |
| `DeviceID` (`0x30`) | `Md5Value(strings.Replace(UUIDOne, "-", "", -1))` | calls `strings.Replace` at `0xb9d780`; dash literal `0xf60309`, length 1; `Md5Value` at `0xb9d785` |
| `DeviceName` (`0x40`) | `comm.LoginData.DeviceName` (`state + 0xa8`) | `0xb9d7e4`; `LoginData` layout |
| `DeviceMac` (`0x50`) | zero-value string | no write |
| `TimeZone` (`0x60`) | literal `"8.00"` | `0xb9d894`; exact bytes `0xf615f6`, length 4 |
| `Language` (`0x70`) | literal `"zh_CN"` | `0xb9d894`; `0xf62229`, length 5 |
| `DeviceBrand` (`0x80`) | literal `"Apple"` | `0xb9d894`; `0xf62238`, length 5 |
| `RealCountry` (`0x90`) | literal `"CN"` | `0xb9d894`; `0xf6040c`, length 2 |
| `IphoneVer` (`0xa0`) | `comm.LoginData.RomModel` (`state + 0x2a8`) | `0xb9d894`; layout |
| `BundleID` (`0xb0`) | literal `"com.tencent.xin"` | `0xb9d8f0`; `0xf6ddcd`, length 15 |
| `OsType` (`0xc0`) | `comm.LoginData.DeviceType` (`state + 0x90`) | `0xb9d928`; layout |
| `AdSource` (`0xd0`) | zero-value string | writes zero to words `0x1a..0x1b` |
| `OsTypeNumber` (`0xe0`) | `comm.LoginData.OsVersion` (`state + 0x2d8`) | `0xb9d8f0`; layout |
| `CoreCount` (`0xf0`) | integer `3` | `0xb9d928` |
| `CarrierName` (`0xf8`) | UTF-8 literal `"中国电信"` | `0xb9d928`; `0xf6ab95`, length 12 |
| `SoftTypeXML` (`0x108`) | zero-value string | no write |
| `ClientCheckDataXML` (`0x118`) | zero-value string | zeroed at words `0x23..0x24` |
| `GUID1` (`0x128`) | first `gogf/guuid.New().String()` | calls `0x7bb540`, `0x7badc0` at `0xb9d996/0xb9d9ad` |
| `GUID2` (`0x138`) | second independent `gogf/guuid.New().String()` | `0xb9d9e1/0xb9d9f8` |
| `Sdi` (`0x148`) | `Md5Value(third independent guuid.New().String())` | UUID calls `0xb9da2c/0xb9da43`; MD5 call `0xb9da48` |

`RandomUUID` (`0x7c1a40`) is a thin wrapper: it calls
`github.com/gogf/guuid.New` (`0x7bb540`) then
`github.com/google/uuid.UUID.String` (`0x7badc0`).  The static evidence does
not identify the random reader selected inside `guuid.New`; it does establish
the output format is the library's canonical UUID string.  Persistent-profile
creation can call a standard UUID generator once per field, then retain the
result; this constructor itself does not require a service token.

`Md5Value` (`0x7bf540`) converts its string argument to bytes, calls
`crypto/md5.Sum` (`0x754aa0`), then `fmt.Sprintf` with exact format `%x`
(`0xf603e0`, length 2).  It therefore returns lowercase hexadecimal MD5 (32
characters).  This confirms both the `DeviceID` and `Sdi` algorithm rather
than merely the callee name.

### `IOSUuid` is separate from this constructor

`baseutils.IOSUuid` is at `0x7c0a00`, but **is not called by**
`createDeviceInfo@0xb9d700`.  It accepts a string and makes two deterministic
MD5-derived, hyphenated identifiers: `MD5ToLower(input + "SM2020032204320")`
and `MD5ToLower(input + "BM2020032204321")`, then slices each 32-character
digest at offsets `0, 2, 3, 1, 0x14` with lengths `8,4,4,4,12` and formats
`%s-%s-%s-%s-%s`.  The two salts are exact bytes at `0xf6dddc` and `0xf6ddeb`;
the format is `0xf6cc31`.  Its ABI returns the two strings (not a token).  The
slice sequence and calls are visible at `0x7c0a2d..0x7c0d90`.

## Device type and time fields

`InstallTime` (`0x158`) and `KernBootTime` (`0x160`) are unix seconds.  The
constructor converts the returned Go `time.Time` to seconds at
`0xb9daa6..0xb9ddd7` by applying Go's wall-time/epoch conversion, then stores
seconds only.

| `LoginData.DeviceType` | InstallTime helper | KernBootTime helper | Timestamp passed to the file-stat helpers |
|---|---|---|---|
| `car` | `GetRandomTimeInIOSWeChat8059` `0x7c2760` | `GetRandomTimeInPastWeek` `0x7c26a0` | `GetRandomTimeInOS18_4_1` `0x7c2220` |
| `ipad` | same iOS helpers | same | same |
| `iphone` | same iOS helpers | same | same |
| `mac` | `GetRandomTimeInMacOSWeChat3_8_10` `0x7c28e0` | `GetRandomTimeInPastWeek` | `GetRandomTimeInMacOS15_6_1` `0x7c23a0` |
| `win` | `GetRandomTimeInWinWeChat3_9_12` `0x7c2a60` | `GetRandomTimeInPastWeek` | `GetRandomTimeForWin` `0x7c2520` |
| other | zeros | zeros | zero |

For the iPad path, each named helper is exactly a date interval sampler:

* `GetRandomTimeInIOSWeChat8059` (`0x7c2760`) samples from
  `2025-04-30 00:00:00` through before `2025-05-30 23:59:59` using
  `start.Add(rand.Int63n(end.Sub(start)))`.  Date construction is
  `0x7c2772..0x7c27e1`; sampling is `0x7c2872..0x7c28b1`.
* `GetRandomTimeInOS18_4_1` (`0x7c2220`) uses start
  `2025-04-20 00:00:00` and end `2025-05-10 23:59:59`, with the same
  `Int63n(end.Sub(start))` form (`0x7c2232..0x7c2371`).
* `GetRandomTimeInPastWeek` (`0x7c26a0`) computes
  `start = time.Now().AddDate(0,0,-7)` then adds
  `rand.Intn(86401) * time.Second` (`0x7c26b2..0x7c2746`).  Despite its name,
  that is the first 86,401 seconds after the seven-day-ago boundary, so its
  output is roughly six to seven days ago, not uniformly across seven days.

All three call `math/rand.Seed` (`0x5df540`) with the current `time.Now()`
value converted to Unix nanoseconds immediately before sampling.  This is the
global pseudo-random source and is not cryptographic randomness.  `time.Date`
receives global location `0x1a84340`; static evidence does not resolve its
runtime location value, so a reproduction must choose the same configured
local time zone or make it an explicit profile parameter.

## File-stat helpers and exact data layout

The helpers fill five `*baseinfo.Stat` fields and one `*baseinfo.Statfs` field
of `DeviceInfo`.  `baseinfo.Stat` is type `0xeb12e0`, size 72:

```
Inode u64 @0x00
Statime {Sec u64, Nsec u64} @0x08
Stmtime {Sec u64, Nsec u64} @0x18
Stctime {Sec u64, Nsec u64} @0x28
Stbtime {Sec u64, Nsec u64} @0x38
```

`GenSysverplist` (`0xb9df80`), `GenDyldcache` (`0xb9e060`),
`GenEtcgroup` (`0xb9e260`), and `GenEtchosts` (`0xb9e340`) are byte-for-byte
the same algorithm.  Given `T` in the incoming Go ABI `RAX`, they seed
`math/rand` from current Unix nanoseconds, allocate `Stat`, set
`Inode = rand.Int63()`, and set all four times to `{Sec:T, Nsec:0}`.  See the
four repeated bodies at `0xb9df97..0xb9e039`, `0xb9e077..0xb9e119`,
`0xb9e277..0xb9e319`, and `0xb9e357..0xb9e3f9`.

`GenVar` (`0xb9e140`) takes the same `T` and sets:

```
Inode                    = 2
Statime, Stbtime         = { T - 2,592,000, rand.Int63() }
Stmtime, Stctime         = { unixSeconds(GetRandomTimeInPastYear()), rand.Int63() }
```

The two random values are independent consecutive `Int63` calls
(`0xb9e1b7`, `0xb9e1c1`).  `2,592,000` is exactly 30 days.  The past-year
helper (`0x7c2160`) uses `now.AddDate(-1,0,0)` then
`now.AddDate(-1,0,0).AddDate(0,0,rand.Intn(30)+1)`; the operand setup is
`0x7c2172..0x7c2208`.  Thus it chooses a day 1--30 after the one-year-ago
date, preserving the time-of-day from `now`.

`GenApfsStat` (`0xb9e420`) fills `baseinfo.Statfs` (type `0xec9080`, size 72):

```
Type        = 0x1a
Fstypename  = "apfs"
Flags       = 0x5480d009
Mntonname   = "/"
Mntfromname = fmt.Sprintf("com.apple.os.update-{%s}@/dev/disk0s1s1", RandomSmallHexString(40))
Fsid        = 0x1a32000001
```

The literal sources are `0xf61632` (four bytes), `0x13ad4f4` (one byte), and
`0xf8a65e` (39-byte format).  `RandomSmallHexString` at `0x7c1a80` allocates
the requested length, seeds a local `math/rand.rngSource` with current Unix
nanoseconds, and writes one character per iteration from a runtime-selected
16-character alphabet through `(*Rand).Intn(16)`; see
`0x7c1a92..0x7c1c0a`.  The alphabet is loaded from a runtime string value at
`0x13bb350`, so this static export does not expose its exact bytes.  It must be
recovered before claiming byte-for-byte equivalence; the requested length of
the APFS suffix is unambiguously 40 at `0xb9e4cb`.

## Practical implementation boundary

The constructor's static generation logic is sufficient to form a persistent
local profile: retain UUIDOne, DeviceID, GUID1, GUID2, Sdi, selected model,
and all generated stat values together.  It does not produce, sign, or
validate a service-issued device token.  In particular, this report makes no
claim that matching these fields authorizes a login or substitutes for the
separate device-token flow.

The remaining bounded offline items are: recover the 16-byte alphabet behind
`RandomSmallHexString`, resolve the runtime time zone stored at `0x1a84340`,
and verify the exact random reader used internally by `gogf/guuid.New`.  Their
addresses and callers are identified above, so none requires speculative
default values.

## Identity separation observed in `UpdateIpadLoginData`

The caller convention also matters for persistence.  `Login.UpdateIpadLoginData`
at `0xb98c80` does not assign either `comm.LoginData.Deviceid_str` (`+0x68`) or
`Deviceid_byte` (`+0x78`).  At `0xb98f04..0xb98f0c` it only reads the existing
string, calls `baseinfo.IOSUuid` (`0x7c7860`), and uses its two returned strings
in the constructed `SoftType` XML written at `LoginData + 0x2c8`
(`0xb99012..0xb99035`).  The function does not connect either login-identity
field to `DeviceInfo.DeviceID`; that latter value is independently derived
from `UUIDOne` as documented above.  The actual producer of the login
string/byte pair is outside this function and remains a separate bounded
static lookup.  A caller should therefore retain an already established
login-identity pair and must not overwrite it merely to match this profile
constructor.


后续补证：RandomSmallHexString 使用的 alphabet 不是 `0x13bb350`（该地址是 Rand 的 itab）。实际字符串 header 从 `0x1a89630` 读取，pointer `0x1aaa7d0`、长度16，精确内容 `0123456789abcdef`；加载在 objdump `0x7c1bca/0x7c1bd6`。生成器已采用此已核对 alphabet。时区作为生成参数显式提供；UUID 使用标准 RFC4122 v4 输入源，未声称复现原进程内部随机种子或时序。
