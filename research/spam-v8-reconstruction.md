# GetNewSpamDataV8 and GetFileInfo reconstruction

Scope: the pinned Go ELF, SHA-256
`9b0f99ad74f11426aeb50b1c543cfadf3562338336d15dd4bc20e306089b0b2f`.
This work statically reconstructed the complete selected builder's field
construction and serialization. It did not execute the original application or
library, read account secrets, or make network requests. Whole-function native
runtime parity and WeChat authorization acceptance remain unverified.

## Implementation and input contract

`internal/deviceproof/spam*.go` provides:

- Exact typed `SpamBody`, `SpamFileInfo`, `SpamFileStat`, `SpamStatFS` schemas and
  `MarshalBinary`, with pointer presence, signed varints, repeated ordering,
  required-field checks and size bounds.
- `GetFileInfo(GUID2, context)`: the 15 selected app images, build UUIDs and exact
  source per-byte XOR/rune conversion.
- `BuildSpamBody(SpamInput)`: all assignments made by the selected
  `GetNewSpamDataV8`. Optional fields not assigned by this function remain absent.
- `MarshalSpamCheckData(body, timestamp)`: the required intermediate
  `NewClientCheckData` envelope.
- `SpamContext`, `SpamDilate32`, `SpamXORString`, `SpamRQTHash`,
  `SpamFileInfoCRC`: separately usable recovered computations.

The builder requires explicit `deviceprofile.Profile`, login identity, device
info name, ROM model, first/second clock readings, server-issued device token and
build constants. The login identity must yield the profile's IDFV/IDFA through
`deviceprofile.IdentityPair`; mismatches are rejected. Keeping ROM model and the
two name inputs separate preserves the actual source objects rather than
trusting misleading protobuf field names.

`SelectedSpamBuild` exposes the exact file-backed constants recorded in
`evidence/spam-build-constants.json`. It does not claim these identify a real
host or that file-backed data establishes the complete runtime initialization.
The parent profile generator separately restores the selected client's virtual
profile rules. No server-issued token is generated here: token provenance must
be established by the caller's actual successful fingerprint response.

## Corrections to the earlier dataflow audit

The older `newspam-v8-dataflow.md` deliberately retained unknowns. Several are
now resolved, and three initially plausible readings were contradicted by the
complete static data flow:

1. The early byte-XOR key is `uint32(firstUnixSeconds * -19 + 7)`. All affected
   strings iterate **bytes**, XOR the low key byte, then convert each result via
   Go `string(rune)`. Values >=128 expand to two UTF-8 bytes. This differs from
   XORing a UTF-8 string in place. `KeyHash` is bit dilation of the low key byte.
2. The later `v08.EncodeString` / `EncodeUInt64` timestamp argument is the
   original Unix seconds at stack slot `-0xc68`, not the derived byte-XOR key.
   The `-0x9c8` stack slot is later reused for another time value; treating that
   slot as a permanent semantic variable gives the wrong answer.
3. `SpamBody.DeviceToken` (tag 64) is the actual token string at
   `LoginData+0x278 -> TrustResponse+0x30 -> TrustResponseData+0x30`, byte-XOR
   encoded. An earlier read of soft-data bytes is not the source of this final
   assignment. The decisive token-string loop appears in the out-of-line
   `0xb4543b..0xb454cf` region reached from `0xb43676`.
4. The body is not immediately compressed. `0xb4510e` marshals it, `0xb45157`
   calculates CRC32 IEEE, and `0xb451be` constructs `NewClientCheckData` with
   `{C32CData: int64(crc32(body)), TimeStamp: int64(thirdUnixSeconds), DataBody:
   body}`. `0xb45238` marshals this second protobuf. Only then are `Compress`,
   `EncodeZipData` and `DeviceRunningInfoNew` applied.

The exact full chain is:

```
SpamBody protobuf
  -> NewClientCheckData{CRC32 IEEE, separate Unix timestamp, body bytes} protobuf
  -> v08.Compress
  -> sae08.EncodeZipData
  -> DeviceRunningInfoNew{Version="00000008",Type=1,EncryptData=...,Timestamp=
     fourthUnixSeconds,Unknown5=5,Unknown6=present zero} protobuf
```

The existing `BuildRunningInfo` can implement the last three steps when given
the `MarshalSpamCheckData` result and the explicit outer timestamp.

## Schema authority

Go ABI type metadata establishes these exact field layouts:

| Native type address | Reconstructed type |
| --- | --- |
| `0xf582e0` | `SpamBody` |
| `0xeb4d80` | `SpamFileInfo` |
| `0xecb280` | `SpamFileStat` |
| `0xef74a0` | `SpamStatFS` |

`scripts/spam_schema.py` reads the frozen metadata and creates the typed Go
encoder plus `evidence/spam-schema.json`. It does not copy original source.
It handles all 95 fields across these four schemas. Required zero-valued pointers
are serialized; an absent pointer is rejected for required fields, and optional
absent pointers remain absent. Strings retain their actual bytes, including
non-UTF8 output of the explicit-length v08 primitive.

## Initial body fields and source objects

The copied struct constructed before `0xb42c58` establishes field order and
presence without relying on write-barrier decompiler guesses:

| Fields | Recovered source/computation |
| --- | --- |
| 1, 2, 3 | 1, first Unix seconds, dilated low XOR-key byte |
| 11, 12 | byte-XOR `yes` |
| 13, 14, 15 | DeviceInfo.OsTypeNumber, DeviceInfo.DeviceName, dilated CoreCount |
| 16, 17 | byte-XOR IOSUuid(LoginData.Deviceid_str) outputs |
| 18, 19, 20, 21 | carrier, 1, [`en0`,`utun3`], 1 |
| 22, 23, 24 | `com.tencent.xin`, LoginData.DeviceName, LoginData.RomModel |
| 25, 26 | dilated uint32 LoginData.ClientVersion |
| 27, 28, 29, 30, 31 | 0, 4, byte-XOR `zh`, byte-XOR `CN`, 4 |
| 32 | byte-XOR `/var/mobile/Containers/Data/Application/{GUID1}/Documents` |
| 33, 34 | allocated zero int32 values |
| 35, 36 | byte-XOR HeadMD5 global and first IOSUuid output |
| 37, 38, 39 | allocated empty strings |
| 40, 41, 42 | byte-XOR `微信`, `/usr/bin/ssh`, `/tmp/test.txt` |
| 43, 44, 45 | allocated empty strings |
| 46 | GetFileInfo(GUID2, XOR key) |
| 47, 50 | allocated empty string, allocated int32 zero |
| 51, 52, 56 | byte-XOR SDI; twice the same direct rqtx(SDI), bit dilated |
| 53, 54, 55, 57 | InstallTime, KernBootTime, allocated uint64 zeros |
| 58, 59 | DeviceInfo.DeviceID byte-XOR; dilated CRC32 IEEE of its raw bytes |
| 60 | decimal dilated CRC32 of the separate plaintext app-image sequence |
| 61, 62 | byte-XOR `2FFC7F6DFEEFFF2B3FFCA029`, `0x1051151051115115` |
| 63, 64 | empty string; byte-XOR server token string |

`SpamDilate32` places input bit i into output bit 2*i. The source emits two
interleaved loops for even and odd input-bit indices; the compact loop preserves
that exact operation.

The rqtx wrapper at `0x7bd5a0` passes SDI directly to the Lua-facing rqtx
function. It does **not** MD5 SDI first. `SpamRQTHash` calls the existing bounded,
previously native-vector-verified `rqtx.SumHex` and then dilates. Its explicit
32-lowercase-hex contract is narrower than the unknown full wrapper domain and
matches the restored profile generator's SDI output.

## File information and CRC ordering

`GetFileInfo` at `0xb45500` builds fifteen pairs of paths and UUIDs, in the exact
order retained in `evidence/spam-fileinfo-literals.json`. Each path is:

```
/var/containers/Bundle/Application/{GUID2}{fixed WeChat.app image suffix}
```

The UUID beside it is a fixed image build UUID. The returned protobuf uses
optional strings tag 1 (path), tag 2 (UUID), both present after the byte-XOR/rune
transform. These are virtual client image records, not filesystem observations
of the Windows/Linux host.

The separate `AppFileInfoCrc` sequence at `0xb42cd3..0xb434b0` uses the same
ordering but prefixes each path with `/private`; plaintext path, UUID pairs are
joined with no delimiter. Its CRC32 IEEE is dilated and rendered as decimal
via fmt `%v` with an int64 argument. This deliberately differs from hashing the
encoded FileInfo protobuf or the unprefixed paths.

## Later encoded fields

Later `v08` transformations use first Unix seconds as timestamp. The exact
source constants and mappings are implemented in `spam_build.go`:

- 70 uses the second clock seconds and second-clock microsecond-derived shift,
  then XORs microseconds, clears `0x3c0` and sets `0x40`. Fields 71/72 use
  **first-clock** nanoseconds /1000 and `usec*0x68323+17` with uint32 overflow.
- 82/83/84 encode the Changed.Seconds of system-version, dyld cache and var,
  multiplied by 1e9. Nanos are intentionally omitted for these three fields.
- 85/86 are present empty string and zero uint64. 87 is unassigned/absent.
- 88 contains one complete encoded StatFS with constants per field; 89 contains
  its independent checksum: LE64 flags, type, FSID, followed by raw TypeName,
  MountOn, MountFrom. CRC32 IEEE is rotated left once, XORed with first seconds
  and `0xc5b26fe3`, then rotated left once again.
- 90 is encoded fixed one. 91 has six encoded appex names and 93 fourteen
  encoded framework names. Exact lengths include the selected source's spelling
  `owl.framework.framework`; it is preserved rather than corrected.
- 94 contains Birth/Changed/Modified seconds*1e9+Nanos of var, encoded with
  `0x2d6aca52`. Overflow is modulo uint64, preserving the source's virtual stats.
- 95 encodes InstallTime*1e9+`0x12241352`. 96/97/98 encode fixed one. 99 is absent.
- 103 encodes the GUID1 StoreKit receipt path. 104/107 encode fixed zero.
- 108/109/111/112 encode the selected build globals. 110 encodes RomModel;
  113 encodes DeviceInfo.IphoneVer (profile Model). These protobuf labels are
  not a reason to substitute different source values.

The CRC table's identity is statically proven rather than assumed:
`scripts/spam_crc_xrefs.py` identifies its initializer in `hash/crc32.init`, and
the `0x5f9cc1` loop explicitly uses polynomial `0xedb88320`.

## Validation and remaining limits

Validation passes with `go test ./internal/deviceproof`, `go vet
./internal/deviceproof` and the full module's `go test ./...`. Evidence includes:

- Four independently encoded Python `google.protobuf` proto2 fixtures covering
  all 95 schema fields, required/optional zero presence, signed integers,
  repeated order, strings and nested messages.
- A complete 7143-byte public synthetic-body fixture, independently constructed
  in Python and serialized by `google.protobuf`, matching the Go builder
  byte-for-byte. SHA-256:
  `71cb74d8b30803e1d6c32d75eac8fbaaa9bf965bbdb75c43be35cb22a3f75ad9`.
  The fixture includes distinct input names/ROM model, two distinct time samples,
  and Nanos large enough to exercise intentional uint64 overflow. It uses the
  retained rqtx oracle's all-zero-hex SDI result; it does not run the target.
- Independent Python CRC and context arithmetic, bit-dilation edge values,
  high-byte rune expansion, missing fields, inconsistent identity pairs,
  missing filesystem/token/time, oversized fields and nil repeated entries.

No fields assigned by the selected GetNewSpamDataV8 remain unimplemented.
This is a statement about the selected static data flow, not proof of live
equivalence: compression/table primitives still have their separately documented
validation limits, the rqtx VM retains opaque bytecode, and SAE retains opaque
tables. Actual server-issued token acquisition, source-profile selection,
runtime integration, live response handling and successful authorization belong
to the parent task. No arbitrary placeholder token or synthetic fixture may be
treated as successful device attestation or a usable login session.
