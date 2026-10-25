# Fixed iPad profile and SecManualAuth field restoration

The fixed ELF supplies a consistent default profile:

```text
DeviceType:    iPad Air iPadOS18.4.1
DeviceName:    iPad Air (第7代)
RomModel:      iPad15,6
OsVersion:     18.4.1
ClientVersion: 402668320 (0x18003b20)
```

These are read from exact pointers and byte lengths in the supplied sample,
not inferred from product naming or a guessed hardware family. They are the
sample's defaults, not a statement that they describe the user's actual device
or remain accepted by today's WeChat servers.

## Authority and reproducibility

Artifact: `research/samples/main.elf.bin`, SHA-256
`9b0f99ad74f11426aeb50b1c543cfadf3562338336d15dd4bc20e306089b0b2f`.
This task uses only static ELF reads, existing decompilation and objdump. No
original program/library execution, network request, or secrets access occurred.
No Ghidra project was opened or modified by this subtask.

`scripts/ipad_profile_literals.py` maps ELF PT_LOAD ranges and reads exact
Go string headers. `evidence/ipad-profile-literals.json` retains:

| Global | String pointer / byte length | Value |
|---|---|---|
| `0x1a86130` | `0xf75115` / 21 | `iPad Air iPadOS18.4.1` |
| `0x1a86140` | `0xf71819` / 18 | `iPad Air (第7代)` |
| `0x1a86150` | `0xf6615e` / 8 | `iPad15,6` |
| `0x1a86160` | `0xf6328f` / 6 | `18.4.1` |
| `0x1a860d0` | `0xf7649c` / 22 | `extshort.weixin.qq.com` |
| `0x1a860e0` | `0xf71807` / 18 | `long.weixin.qq.com` |

The DWORD at `0x1aa9c58` is `402668320`. Strings and lengths are not recovered
by searching adjacent substrings: the byte count is part of each claim.

Retained focused disassembly: `ipad-profile-disassembly.txt`,
`ipad-deviceid-disassembly.txt`, `ipad-qr-controller-disassembly.txt`,
`ipad-qr-profile-call-disassembly.txt`, `ipad-iosuuid-disassembly.txt`,
`ipad-md5tolower-disassembly.txt`, `ipad-client-seq-disassembly.txt`,
`ipad-auth-selection-disassembly.txt`, and `ipad-secmanualauth-disassembly.txt`.
Existing `login-ghidra.txt` contains the same auth/constructor functions, but
its historical heading **misnames** `0xb98c80` as GenIpadLoginData. The fixed Go
function table establishes `GenIpadLoginData@0xb986a0` and
`UpdateIpadLoginData@0xb98c80` as distinct functions.

## Which native branch selects iPad

The ordinary `LoginController.LoginGetQR@0xc40580` calls
`Login.GetQRCODE@0xb7dae0` at `0xc407d3`. That path calls
`GenIpadLoginData@0xb986a0` at `0xb7dc73` for a new state, or
`UpdateIpadLoginData@0xb98c80` at `0xb7dc36` for an existing cached state whose
username is nonempty and client version matches the current global.

The original QR argument struct `Login.GetQRReq@0xe94380` contains Proxy,
DeviceID, DeviceName and LoginType. There is no AuthType enum in that struct.
Other native controller entry points exist for AndroidPad, Windows, Car and
Mac; they must not be treated as aliases for the ordinary iPad entry point.
The later iPad `CheckSecManualAuth` calls `SecManualAuth@0xba0f80` directly
at `0xb76460`, passing LoginData. SecManualAuth does not select an iPad model
from a numeric user-facing auth-type argument.

## Constructor and update rules

`GenIpadLoginData` assigns the default DeviceType, current ClientVersion,
requested DeviceName (falling back when empty or literal Swagger `"string"`),
the default RomModel and OsVersion. It derives SoftType after populating
Deviceid_str, RomModel and OsVersion. It creates DeviceInfo separately.

`UpdateIpadLoginData` preserves existing nonempty values:

| LoginData field | Offset | When empty/zero |
|---|---:|---|
| DeviceType | `0x90` | default full DeviceType string |
| ClientVersion | `0xa0` | default version DWORD |
| DeviceName | `0xa8` | supplied request DeviceName, or global if empty/`"string"` |
| ShortHost / LongHost | `0x118` / `0x128` | respective global host |
| RomModel | `0x2a8` | global `iPad15,6` |
| Imei | `0x2b8` | copy existing Deviceid_str |
| SoftType | `0x2c8` | construct XML with baseinfo.IOSUuid and profile values |
| OsVersion | `0x2d8` | global `18.4.1` |
| DeviceInfo | `0x3e8` | call createDeviceInfo with `"iPad"` |

The model write at `0xb98e7b` and OS write at `0xb9907a` are conditional on
their respective string lengths being zero. Neither value is chosen randomly.
An existing cached account can also supply RomModel, OsVersion, DeviceType,
ClientVersion, DeviceInfo and token to CheckSecManualAuth (see writes beginning
`0xb7621d`); freshly generated and existing-state paths must remain distinct.

## Login device identity: string and decoded bytes

UpdateIpadLoginData does **not** regenerate or assign Deviceid_str/Deviceid_byte.
The new-state constructor establishes them from the QR request:

1. GenIpadLoginData loads the supplied DeviceID string. For empty or literal
   `"string"`, it calls `baseutils.CreateDeviceId@0x7be1e0` with empty input.
2. A nonempty input with at least two bytes is normalized to begin with ASCII
   `"49"`: when its first two bytes differ, the constructor replaces them
   (`"49" + supplied[2:]`), rather than adding two bytes before the whole input.
3. `encoding/hex.DecodeString@0x736360` decodes that resulting string. The
   constructor stores the original normalized string at LoginData `+0x68`
   (length `+0x70`) and the decoded slice at `+0x78` (length `+0x80`).

For the current 16-byte identity, **hex encoding those same persistent bytes**
is the matching 32-character Deviceid_str. Its raw first byte is `0x49`; the
ASCII string begins `"49"`. These are two representations of the same login
identity, not independently generated values. Keep that identity stable from
QR generation through authorization. Do not substitute DeviceInfo.DeviceID's
separate hash into these fields.

The fallback CreateDeviceId hashes its supplied/generated seed with MD5,
encodes the digest in lowercase hex, and replaces the first two characters
with `"49"`. Its empty seed path obtains a 15-character random string. Exact
seed distribution is not needed to preserve an already established identity.
The constructor's malformed-short-string handling is a native bounds panic,
not a useful supported input rule.

## Two different IOSUuid functions

There are two similarly named functions in this ELF, with **different** salts
and formatting. They must not be conflated:

| Function | Purpose here | Salts / format |
|---|---|---|
| `baseutils.IOSUuid@0x7c0a00` | DeviceInfo / fingerprint and spam | separate 15-byte salts, `%s` formatting; existing Profile identity pair |
| `baseinfo.IOSUuid@0x7c7860` | authorization SoftType and AdSource | 14-byte salts below, `%x` formatting of strings |

For **baseinfo.IOSUuid**, the exact salts are:

- `0xf6cc93`, length `0xe`: `"SM202003220432"`
- `0xf6cca1`, length `0xe`: `"BM202003220432"`

Each is appended to LoginData.Deviceid_str. `MD5ToLower@0x7be920` returns the
32-character lowercase **hex string** of MD5. Each digest string `s` is sliced
as `s[:8]`, `s[2:6]`, `s[3:7]`, `s[1:5]`, `s[20:32]`. Each slice goes through
`runtime.convTstring@0x4795c0`, establishing that these are strings, and the
format at `0xf6cc77` / 14 bytes is `"%x-%x-%x-%x-%x"`.

Go's `%x` encodes the bytes of each hex-text slice a second time. Therefore
each returned auth identity has 68 characters (64 hex digits plus four
hyphens), not 36 characters. An equivalent newly written implementation is:

```go
sum := md5.Sum([]byte(loginDeviceID + salt))
s := hex.EncodeToString(sum[:])
id := fmt.Sprintf("%x-%x-%x-%x-%x", s[:8], s[2:6], s[3:7], s[1:5], s[20:32])
```

This **does not justify changing** the separate baseutils identity algorithm
used for Profile.IDFV/IDFA. The salt ending in `0`/`1` belongs to that other
function, not the baseinfo auth function examined here.

`scripts/ipad_auth_identity_vectors.py` creates two synthetic examples using
Python's independent MD5/string-byte codec. `ipad_auth_identity_check.go`
verifies those examples with Go's actual `%x` string formatting semantics.
`evidence/ipad-auth-identity-vectors.json` records that these are static-formula
results, not observed outputs of the original target.

## Native SoftType XML

Let `a,b = baseinfo.IOSUuid(LoginData.Deviceid_str)` as defined above. If state
SoftType is nonempty, the auth request preserves it. Otherwise the native code
concatenates these nine string pieces, without additional escaping:

```text
<softtype><k3>
{state.OsVersion}
</k3><k9>iPad</k9><k10>6</k10><k19>
{a}
</k19><k20>
{b}
</k20><k22>(null)</k22><k33>微信</k33><k47>1</k47><k50>1</k50><k51>com.tencent.xin</k51><k54>
{state.RomModel}
</k54><k61>2</k61></softtype>
```

The newlines above show the nine concatenated pieces; no newline is inserted
into the actual XML. For the default profile and the public synthetic test
identity `490102030405060708090a0b0c0d0e0f`, the fixture contains the resulting
334-byte UTF-8 string. DeviceName is not encoded inside this XML.

## SecManualAuth non-proof mapping

The recovered Go ABI offsets and protobuf tags are retained in
`evidence/ipad-auth-layouts.json`, independently decoded by
`scripts/ipad_auth_layouts.py`. This table gives the actual assignments, rather
than every field the schema could potentially carry:

| ManualAuthAesReqData tag / name | Native value |
|---|---|
| 1 BaseRequest | random AES SessionKey; present zero Uin; existing decoded Deviceid_byte; state ClientVersion; full DeviceType bytes; Scene=1 |
| 2 BaseReqInfo | allocated empty BaseAuthReqInfo message; no AuthReqFlag or AuthTicket assigned |
| 3 Imei | **Deviceid_str**, irrespective of the separate state.Imei field |
| 4 SoftType | preserve state SoftType, otherwise XML above |
| 5 BuiltinIpseq | present zero uint32 |
| 6 ClientSeqId | Deviceid_str + `"-"` + decimal Unix seconds; helper `0x7be520` |
| 8 DeviceName | state.DeviceName, normally `"iPad Air (第7代)"`; custom nonempty names are preserved |
| 9 DeviceType | exact four-byte string `"iPad"`, distinct from BaseRequest DeviceType |
| 10 Language | `"zh_CN"` |
| 11 TimeZone | `"8.0"` |
| 13 Channel | present zero int32 |
| 14 TimeStamp | low uint32 of current Unix seconds |
| 15 DeviceBrand | `"Apple"` |
| 17 Ostype | state.DeviceType, full `"iPad Air iPadOS18.4.1"` by default |
| 18 RealCountry | `"CN"` |
| 19 BundleId | `"com.tencent.xin"` |
| 20 AdSource | **first** result of baseinfo.IOSUuid(Deviceid_str), 68 characters |
| 21 IphoneVer | **global** RomModel `"iPad15,6"`, even if a cached state's model differs |
| 22 InputType | 2 |
| 24 ExtSpamInfo | actual separately constructed WCExtInfo buffer |

Tags 7 Signature, 16 DeviceModel and 23 ClientCheckData are **not assigned** by
this fixed SecManualAuth function. They stay absent. In particular, a missing
ClientCheckData is not an established deviation from the fixed sample.
RomModel does not belong in tag 16 merely because the field's name appears
plausible; this sample sends it in tag 21.

The first-stage implementation should replace its guessed `"iPad13,1"` with
the recovered global model, use the persistent profile's DeviceName/DeviceType
and correctly derive SoftType/AdSource from the unchanged login identity.
Matching these fields establishes a source-backed request shape, not server
acceptance or a cure for return code `-106`.

## Checks and remaining limits

Passed reproducible checks:

```powershell
python research/scripts/ipad_profile_literals.py
python research/scripts/ipad_auth_layouts.py
python research/scripts/ipad_auth_identity_vectors.py
go run research/scripts/ipad_auth_identity_check.go
```

Exact literals, branch instructions, direct assignments and formatting are
verified from the fixed file. Global configuration overrides or cached state
can change selected values at runtime; those branches are described above.
No claim of original-runtime output parity, actual device identity, successful
login, or delivered messages follows from this static profile work.
