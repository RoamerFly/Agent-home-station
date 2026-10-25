# Version-8 WCSTF / WCSTE reconstruction

`internal/deviceproof/running.go` implements the inner running-info protobufs,
their zlib and SAE encoding chain, and the outer `DeviceRunningInfoNew`
protobuf. It makes no network calls, samples no hidden clock or random source,
reads no account/session files, and never executes the original native program.
The caller supplies time samples, the WCSTF count/deltas, and a parsed SAE table.

The implementation restores this chain from the fixed ELF:

```text
WCSTF or WCSTE protobuf
    -> v08.Compress (normal zlib stream)
    -> sae08.Cipher.EncodeZipData
    -> DeviceRunningInfoNew protobuf
```

Completion of this chain is not evidence that WeChat accepts a device proof or
that a sending session exists. Compression is format-compatible; byte-for-byte
equivalence of Go's DEFLATE output and the bundled zlib is unverified.

## Static sources and scope

The authoritative functions are `GetWcstf08@0x7cc3a0` and
`GetWcste08@0x7cc820` in `research/samples/main.elf.bin`, SHA-256
`9b0f99ad74f11426aeb50b1c543cfadf3562338336d15dd4bc20e306089b0b2f`.
`research/evidence/device-v8-ghidra.txt` retains their decompilation. This task
also read their x86-64 instructions with `objdump` to correct the decompiler's
32-bit-looking `int` temporaries in the actual 64-bit clock arithmetic.

The protobuf schema comes from the fixed Go runtime ABI struct tags, printed by
`research/scripts/recover_device_v8_structs.py`:

| Message / ABI type | Tag | Type | Presence |
|---|---:|---|---|
| WCSTF / `0xedf020` StartTime | 1 | uint64 | required |
| WCSTF CheckTime | 2 | uint64 | required |
| WCSTF Count | 3 | uint32 | required |
| WCSTF EndTime | 4 | uint64 | repeated, unpacked |
| WCSTE / `0xf1efa0` CheckId | 1 | string | required |
| WCSTE StartTime / CheckTime | 2 / 3 | uint32 | required |
| WCSTE Count1 / Count2 / Count3 | 4 / 5 / 6 | uint32 | required |
| WCSTE Const1..Const6 | 7..12 | uint64 | required |
| DeviceRunningInfoNew / `0xef7360` Version | 1 | bytes | required |
| DeviceRunningInfoNew Type | 2 | uint32 | required |
| DeviceRunningInfoNew EncryptData | 3 | bytes | required |
| DeviceRunningInfoNew Timestamp | 4 | uint32 | required |
| DeviceRunningInfoNew Unknown5 / Unknown6 | 5 / 6 | uint32 | required |

The scalar fields are allocated pointers in the original code. Required zero
values are therefore present on the wire. WCSTE tags 4 and 6 and outer tag 6
must be emitted as `20 00`, `30 00`, and `30 00` respectively. A zero WCSTF
count emits tag 3 and no repeated tag 4. This resolves the earlier audit's
presence uncertainty from struct tags and an independent proto2 serializer.

## Exact clock recovery

The fixed `time.Now@0x4eecc0` returns Go Time's wall word in RAX and ext word in
RBX. At `0x7cc3cf`, `bt rax,63` checks whether wall includes monotonic data. If
set, `rax<<1>>31` extracts wall seconds and the code adds `0xdd7b17f80`
(`59453308800`, Go `wallToInternal`, seconds to 1885). Otherwise RBX already
holds seconds from Go's internal year-1 epoch. Low 30 wall bits are nanoseconds.

The instructions at `0x7cc3f3..0x7cc428` are:

```text
rdx = internalSeconds * 1_000_000_000       (64-bit wrap)
rcx = rdx + int64(wall & 0x3fffffff)
rcx += 0xa1b203eb3d1a0000                  (64-bit wrap)
rdx:rax = signedMultiply(rcx,0x431bde82d7b634db)
millis = (rdx >> 18) - (rcx >> 63)
```

`0xa1b203eb3d1a0000` equals
`-62135596800 * 1_000_000_000 modulo 2^64`, converting Go's internal seconds
to Unix nanoseconds. The signed magic-number division is division by 1,000,000
with truncation toward zero. Thus the exact expression is
`(now.Unix()*1e9 + int64(now.Nanosecond())) / 1e6`, with the original signed
64-bit overflow retained. It is the inlined `UnixNano()/1e6` operation.

For contemporary positive dates this equals `now.UnixMilli()`. These are not
universally interchangeable: at Unix time `-1 ns`, signed nanosecond division
gives `0`, while `UnixMilli()` gives `-1`. At year 2500 the original nanosecond
multiplication wraps; the reconstructed value is `-1721518473709`, rather than
wide-range milliseconds. Tests cover both differences and a real monotonic
`time.Now` value. The wide-range behavior is only reconstructed machine
arithmetic, not a claim that Go documents UnixNano outside its valid range.

WCSTF stores the resulting signed 64-bit bit pattern in the required uint64
StartTime/CheckTime pointers (`mov QWORD` at `0x7cc504` / `0x7cc520`). The
outer Timestamp writes only ECX (`mov DWORD [rax],ecx` at `0x7cc692`), so it is
**uint32(millis)**, modulo `2^32`, not seconds or clamped milliseconds.

WCSTE makes two calls to `time.Now`, at `0x7cc83a` and `0x7cc861`:

1. First call: recover internal seconds, then add `0xfffffff1886e0900`
   (`-62135596800`), yielding Unix seconds. Inner tags 2/3 and outer tag 4
   store low 32 bits (`0x7cc91d`, `0x7cc938`, `0x7ccc49`).
2. Second call: the same signed nanosecond-division expression as WCSTF,
   at `0x7cc8a1..0x7cc8d6`; full 64-bit bit pattern goes into Const2..Const5.

`BuildWCSTEAt(secondsNow,millisNow,cipher)` preserves these separate samples,
including crossings of second boundaries. `BuildWCSTE(now,cipher)` is a
convenience that deliberately supplies the same explicit sample twice.

## WCSTF inputs and sequence

The login caller passes the Go username string pointer in RAX and byte length
in RBX. The callee saves both, and reloads RBX's original value for its loop
count. It does not use string bytes in the recovered body. Therefore the
caller should supply `uint32(len(wxid))`, not rune count, a fixed guess, or the
integer value of the string pointer. Username selection belongs to the caller.

Starting at the recovered millisecond value, each original
`math/rand.Int63n(10000)` call adds an integer in `[0,9999]`, and the cumulative
value is appended to unpacked EndTime. The reconstructed API takes these
**actual chosen deltas** explicitly; it does not guess the old process's global
random seed/sequence. Supplying a seeded or cryptographic random source is the
caller's policy. Reproducing old random samples is unnecessary for the field
contract and would not establish authenticity of the reported device events.

The API checks exact `len(deltas)==count`, `delta<10000`, and a local count cap
of 4096. That cap is a bounded-allocation constraint above normal username
lengths, not a value recovered from the original code.

## WCSTE constants and outer fields

The exact 11-byte CheckId string at ELF `0xf698b5` is `"<LoginByID>"`.
WCSTE sets Count1=0, Count2=1, Count3=0, Const1=`0x0555015050505051`, and
Const6=`0x0554400000000000`; Const2..Const5 all use the second clock sample's
signed nanosecond division, encoded as uint64 bit patterns.

Both outer messages use Version=`"00000008"` (8 bytes), Type=1,
EncryptData=`EncodeZipData(zlib(inner))`, Unknown5=5, Unknown6=0. Timestamp
uses the explicitly recovered units above. The two messages returned by the
complete builders are suitable as the bytes inside WCExtInfo's respective
buffer containers; no extra custom framing or base64 is added.

## APIs

```go
RunningMillis(now time.Time) int64
MarshalWCSTF(now time.Time, count uint32, deltas []uint32) ([]byte, error)
MarshalWCSTE(secondsNow, millisNow time.Time) []byte
BuildRunningInfo(plain []byte, timestamp uint32, cipher *sae08.Cipher) ([]byte, error)
BuildWCSTF(now time.Time, count uint32, deltas []uint32, cipher *sae08.Cipher) ([]byte, error)
BuildWCSTE(now time.Time, cipher *sae08.Cipher) ([]byte, error)
BuildWCSTEAt(secondsNow, millisNow time.Time, cipher *sae08.Cipher) ([]byte, error)
```

The caller loads the supplied table separately, typically using
`sae08.ParsePinned(raw)`. The running-info package does not choose a table file
or silently accept tables from another protocol revision.

## Validation

`internal/deviceproof/testdata/running-static.json` retains an independently
constructed proto2 FileDescriptorProto (base64), its provenance, and six cases
serialized by Python `google.protobuf` 7.36.2 from the recovered ABI tags.
This schema is explicitly **static-derived**, not presented as an extracted
original descriptor. Cases cover contemporary milliseconds, zero/empty repeated
sequence, negative nanosecond division, uint32 seconds rollover, original
64-bit nanosecond overflow, and the two clock calls crossing a second boundary.

Go tests compare inner serialization byte-for-byte with those fixtures, verify
required zero fields, uint32 timestamp truncation and outer metadata using an
independent bounded wire reader, and check actual compression/SAE composition
with the pinned `08sae.dat` research sample. Invalid counts, deltas, nil ciphers,
empty input, and size limits are checked. The static input fixture contains no
account credentials.

Passed:

```text
go test ./internal/deviceproof
go test -race ./internal/deviceproof
go vet ./internal/deviceproof
```

No original-output oracle, authorization success, or real send was obtained by
this subtask. SAE retains the independently supplied opaque table dependency;
see the SAE audit and v08 primitive notes for their separate limitations.
