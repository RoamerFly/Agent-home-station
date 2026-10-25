# Version-8 SAE table transform reconstruction

This is a static reconstruction of the selected Go ELF's table transforms. No
original `main` or `libv08.so` was launched for this work. Native runtime parity
and server acceptance remain unverified. Passing tests below do not establish
successful WeChat authorization.

## Recovered API

`internal/sae08` is a stdlib-only Go package. `ParsePinned(raw)` checks the frozen
08sae.dat SHA-256 before parsing; `Parse(raw)` checks version, mode, field shapes,
duplicates and bounds, but does not authenticate the data's origin. The cipher
owns copies of all necessary data and has no mutable per-call state.

```go
c, err := sae08.ParsePinned(raw08SaeDat)
if err != nil { return err }
zipCiphertext, err := c.EncodeZipData(compressedBytes)
ztCiphertext, err := c.EncryptZT(serializedTokenRequest)
```

`EncodeZipData` does not perform compression. It rejects empty input, accepts at
most 2 MiB, and pads a partial block with the padding length. An already aligned
input gets no additional block. `EncryptZT` applies full PKCS#7, including an
additional block for aligned or empty input; its limit is also 2 MiB. Both start
from the supplied table IV on each invocation and chain ciphertext blocks locally.
Neither entry point includes networking, file discovery or account credentials.

## Artifact and field authority

The data is `research/samples/08sae.dat.bin`, 262387 bytes, SHA-256
`dfedf50d53e3579691bf428fa9686883ffe91b9781fd587e08e9808617a152a3`.
Its provenance is retained in `evidence/sae08-extraction.json`.

The Go metadata for `wechat.SaeInfo` at type address `0xf08400` establishes the
following offsets, rather than inferring meanings merely from byte lengths:

| Wire field | Go struct offset | Name | Size in selected data |
| --- | --- | --- | --- |
| 1 | 0x08 | Type | 8: `00000008` |
| 2 | 0x20 | Iv | 16 |
| 3 | 0x38 | Len | scalar 0x3060 |
| 9 | 0x40 | UnknowValue9 | 128 |
| 10 | 0x58 | TableKey | 147456 |
| 11 | 0x70 | UnknowValue11 | 64 |
| 12 | 0x88 | TableValue | 110592 |
| 18 | 0xa0 | UnknowValue18 | 4096 |

Field 50 is an unknown protobuf varint in this sample, value 3. No encryption
meaning is assigned to it. The parser accepts this unknown scalar but rejects
other unsupported fields and wire types.

`initSaeInfo` at `0x7c92c0` reads `08sae.dat`, protobuf-decodes the above type and
transforms TableKey and TableValue before use. Reading raw field 10/12 as ready
lookup tables would be wrong. `sae08-helpers-ghidra.txt`,
`sae08-struct-layout.txt`, and `sae08-table-init-disassembly.txt` retain evidence.

## Table initialization

The helper at `0x7c1680` reconstructs the key table. For round `r=0..8`, row and
column `a,b=0..3`, input byte `x=0..255`, and output component `k=0..3`:

```
i = r*0x4000 + a*0x1000 + b*0x400 + x*4 + k
key[i] = rawField10[i] XOR field9[(r*a*b + x + k + 7) & 127]
```

The helper at `0x7c1800` reconstructs the value table. For combination stage
`s=0..2`, half `h=0..1`, index `x=0..127`:

```
i = r*0x3000 + a*0xc00 + b*0x300 + s*0x100 + h*0x80 + x
value[i] = rawField12[i] XOR field11[(r*a*b + s*h + x + 23) & 63]
```

The masks follow the exact assembly's `(t+constant)&negativePowerOfTwo`
subtraction, simplified algebraically to a modulo power of two. Field 18 is
not transformed by this initialization.

## Block transform

The selected `EncodeZipData` at `0x7c9bc0` accepts modes 0x3060 and 0x4095. The
implemented profile deliberately accepts only the actual table mode 0x3060;
0x4095's unused branches are not claimed as restored functionality.

1. XOR 16 input bytes with the IV or preceding ciphertext block.
2. Transpose from column-major bytes to four contiguous rows.
3. Shift rows left by row number (0, 1, 2, 3).
4. Perform nine table rounds: use the round's 0x4000-byte key lookup to expand
   16 state bytes into 64 component bytes; combine each group of four using
   three packed-nibble truth tables from the round's 0x3000-byte value table;
   shift rows again.
5. Apply one 256-byte final lookup per state position from field 18 and transpose
   back to output byte order. That ciphertext becomes the next chaining value.

Key expansion indices are established by `GetSecTable` at `0x7c98a0`; packed
combination indices and nibble selection by `GetSecValue` at `0x7c99c0`. Row
rotations come from `ShiftRows` at `0x7c9820` and its helper `0x7c94c0`.

The independent ZT helpers were compared: `shiftKey` at `0x7db2c0` emits the
same row rotations; `chooseKey` at `0x7dad80` emits the same 64-byte component
layout; `chooseKey2` at `0x7db040` and nibble helper `0x7daf80` implement the same
three reverse-order combinations and packed-nibble selection. `ZT.Encrypt` at
`0x7db420` shifts before each of nine rounds and once before final lookup,
equivalent to ZipData's initial shift plus nine shifts after rounds. ZT differs
in always applying a complete PKCS#7 padding operation. Evidence is in
`evidence/zt-ghidra.txt`, `zt-helpers-ghidra.txt`, and
`sae08-zt-nibble-disassembly.txt`.

## Validation and limits

`go test ./internal/sae08` and `go vet ./internal/sae08` pass. Validation covers:

- Eleven public synthetic-input vectors of lengths 1, 15, 16, 17, 31, 32, 33,
  64, 255, 256 and 257 with the frozen tables. A separately written Python model
  uses nested tables and different indexing structure. It agrees with both Go
  padding entry points. `scripts/sae08_reference.py` regenerates this evidence;
  it is not a native execution oracle.
- Artificial unmasked lookup tables implementing ordinary AES. The recovered
  table pipeline matches stdlib `crypto/aes` CBC on partial, aligned and multiple
  blocks, including NIST SP800-38A F.2.1's known first ciphertext block
  `7649abac8119b246cee98e9b12e9197d`. This independently checks orientation,
  expansion/component placement, combination order and block chaining.
- Length/padding boundaries, malformed/duplicate/truncated envelopes, pinned
  digest rejection, caller-buffer ownership and independence across calls.

The package still requires opaque tables obtained from the selected artifact.
It reconstructs their interpretation; it does not recover an upstream secret
key, prove how those tables were generated, establish distribution permission,
or show that any fabricated device data will be accepted. It must not be
reported as full replacement of all device-proof logic or completed login.
