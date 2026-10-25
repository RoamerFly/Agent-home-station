# v08 scalar/string encoding and compression reconstruction

The package `internal/v08` implements the small encoding helpers from the fixed
Linux library in Go source. It does not load the original library, use CGO,
execute the original program, or embed encoding VM instructions. This work is
static reconstruction. No original-library oracle has been run for these helpers,
and the tests do not establish successful WeChat authorization or sending.

## Inputs and retained evidence

- Fixed `research/samples/libv08.so.bin`: SHA-256
  `c89374360a439abb24bf551eda04c3001fda4b58a2bf33d9e610222382796ddf`.
- `evidence/v08-primitives-disassembly.txt`: GNU objdump Intel syntax, ELF virtual
  addresses `0x63a5` through `0x6d82`; no target execution.
- `evidence/v08-primitives-ghidra.txt`: focused native decompilation from a new
  **library-only** project `/tmp/v08-primitives-20261024/primitives`. Ghidra's
  imported image base adds `0x100000` to the ELF addresses. The older library-only
  project referenced in the historical logs was absent on this WSL instance;
  no main-program project was opened or changed.
- `evidence/device-v8-dependencies-ghidra.txt`: original Go wrappers
  `EncodeString@0x7bd760`, `EncodeUInt64@0x7bd9a0`, `Compress@0x7bdd00`.

## Recovered encoding

Write `ROL_w(x,n)` for width-`w` rotate left and `ROR_w` for rotate right.
All operations are on unsigned values of the stated width; x86's 32-bit shift
counts wrap modulo 32. The equivalent Go implementation uses `math/bits`.

`EncodeString(input, constant, timestamp)` is the Go wrapper's argument order.
It invokes native `encode_cstr(input,len,output,constant,timestamp)` at ELF
`0x69ef`. Input length is explicit, so embedded NUL and arbitrary binary bytes
remain data. The output length equals the input length, including zero length.

For each little-endian 32-bit word `x_i`, with `i` starting at zero:

```text
a = ROL_32(x_i, i+1)
b = a XOR ROR_32(timestamp, i) XOR ROL_32(constant, i)
y_i = ROL_32(b, i+1)
```

Native helper `encode_int32_group_bytes@0x65e0` executes this with 32-bit words.
There is no inter-word feedback. Tests include the `i=31,32` rotation wrap.

After all complete words, a tail of zero to three bytes is handled by
`encode_left_bytes@0x63a5`. Its index restarts at zero, independently of the word
count. For each tail byte `x_j`:

```text
a = ROL_8(x_j, j+1)
b = a XOR ROR_8(low8(timestamp), j) XOR ROL_8(low8(constant), j)
y_j = ROL_8(b, j+1)
```

The caller explicitly truncates `constant` to 8 bits for the tail; only the low
8 timestamp bits survive the helper's output byte casts. The final native right
shift uses `j XOR 7`, which equals `7-j` for the only reachable `j=0,1,2`.

`EncodeUInt64(value,constant,timestamp)` preserves the original **Go wrapper**
parameter order. The wrapper calls native
`encode_int64(&value,timestamp,constant,&output)` at ELF `0x6c8c`, reversing the
two scalar arguments. The native helper processes low/high 32-bit words at
indexes 0 and 1. Its `ROR(x,~i)` and `ROR(x,i XOR 31)` both equal
`ROL(x,i+1)` for those indexes, giving exactly the same result as encoding the
8 little-endian input bytes with `EncodeString`. The manual test with distinct
`constant=1,timestamp=0` versus `constant=0,timestamp=1` exposes a parameter swap.

These helpers provide obfuscation only. They do not authenticate a device or
prove ownership of an account.

## Compression wrapper and static libz verification

The original Go `v08.init.1@0x7bdc40` loads the exact 11-byte path
`lib/libz.so` from string address `0xf69f95`. `Compress` invokes the 13-byte
symbol `compressBound` at `0xf6c056` to size its destination, then the 8-byte
symbol `compress` at `0xf66a56`, supplying destination pointer, length pointer,
source pointer and source length. It returns the destination sliced to the
reported length. The wrapper's nonempty source guard is followed by a bounds
panic for empty input; this reconstruction returns `ErrEmptyInput` explicitly.
It also reports Go writer errors instead of ignoring a foreign function status.

`scripts/v08_extract_libz.py` checks the frozen OCI manifest and the SHA-256 of
every layer, inspects all layers for later replacement/whiteouts, and extracts
only the regular `usr/wic-go/lib/libz.so.1.3.1` member. Layer 12 also stores
`usr/wic-go/lib/libz.so` as a **regular file**, not a symlink; its bytes have the
same digest. Neither file is executed. Retained evidence:

- `evidence/v08-libz-extraction.json`: final layer 12, 155,304 bytes, SHA-256
  `97cba22efd8992179623499eeedf447c6bac64bdc849b92f013e8b4ec2e68ffd`.
- `evidence/v08-libz-readelf.txt`: ELF headers and symbols.
- `evidence/v08-libz-compress-disassembly.txt`: `compress@0x4044` calls
  `compress2@0x3e81` with level `-1`; `compressBound@0x4080` computes
  `n + (n>>12) + (n>>14) + (n>>25) + 13`.
- `evidence/v08-libz-init-disassembly.txt`: `compress2` calls `deflateInit_`,
  whose wrapper calls `deflateInit2_` with method 8, windowBits 15, memLevel 8,
  and strategy 0. This selects the normal zlib stream format.

`internal/v08.Compress` uses Go's standard `compress/zlib` default level, producing
an RFC 1950 stream with a DEFLATE payload and Adler-32 trailer. The stream format
and recovered content match the observed call contract. Different DEFLATE
encoders may choose different blocks and match lengths, so **byte-for-byte parity
with the bundled zlib 1.3.1 is not established or claimed**. If an upstream field
authenticates or compares compressed bytes, interoperability still needs actual
protocol evidence. The extracted ELF is evidence only and is not needed by the
application or its Docker image.

## Validation and limits

`scripts/v08_make_vectors.py` independently models the literal x86 shifts,
masked shift counts, ORs, byte truncations, and XORs using Python integers. It
does not use Go's rotate helpers or call a native library. Its retained fixture
`internal/v08/testdata/static-vectors.json` contains 103 binary-string vectors
and 28 integer vectors. Coverage includes lengths 0 through 13, all tail
lengths, word indexes around 31/32, 255/256/257-byte inputs, embedded NUL,
UTF-8 bytes, all-zero/all-one masks, high-bit overflow, and distinct scalar
parameters. These are **static-model vectors**, not observed original outputs.

Passed checks:

```powershell
python research/scripts/v08_make_vectors.py
python research/scripts/v08_extract_libz.py
go test ./internal/v08
go test -race ./internal/v08
go vet ./internal/v08
```

The Go tests also include manually computed bit-rotation/order cases and check
compression headers, absence of a preset dictionary, Adler-32 and full
decompression for binary and repetitive payloads. No account/session files were
read, and no network requests or original target/library executions were made.

This package is one dependency of the device-data path. It does not implement
the WCExtInfo field selection, SAE cipher/table recovery, a device token,
authorization acceptance, or a sending session.
