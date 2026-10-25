# HybridECDH reconstruction

`internal/hybrid` is a pure-Go reconstruction of the iPad `HybridEcdhRequest` / `HybridEcdhResponse` envelope and its CGI 502/503 QR outer packet, with the same envelope also available for CGI 252 SecManualAuth. It has no CGO, no closed executable or shared-library dependency, and no network client.

## Evidence and selected behavior

- `research/evidence/main-functions.json` lists the artifact's `wechatdll/Algorithm.(*Client).encryptoIOS`, `decryptoIOS`, `HybridEcdhPackIosEn`, `HybridEcdhPackIosUn`, `DoECDH415Key`, `HybridHkdfExpand`, and `CalcMsgCrcForData_7019` functions.
- `research/evidence/send-mm-descriptor.json` supplies the field layout for `HybridEcdhRequest`, `HybridEcdhResponse`, and `SKBuiltinBuffer_t`: request fields 1..5 and response fields 1..4.
- `research/evidence/send-crypto-ghidra.txt` records static disassembly/pseudocode for nearby cryptographic and RQTX call paths. It does not establish remote interoperability.
- `research/evidence/login-static-profile.json` records the pinned image's iPad bootstrap P-256 public key and version. The implementation intentionally accepts this key from the caller rather than embedding it.

The inner encoding follows the observed/historical construction: P-256 ECDH X coordinate, SHA-256, a 32-byte random key, HMAC-SHA256 extract keyed by the bytes `security hdkf expand`, HKDF expand to 56 bytes, AES-192-GCM over zlib data, and the `ciphertext || nonce(12) || tag(16)` layout. The request's RQTX value is calculated over the encrypted HybridECDH protobuf, before the outer header.

## Outer packet direction

The request writer emits the observed request fields: `BF`, header length/mode, type/cookie length, supplied client version, UIN, cookie, CGI, two plaintext lengths, the request marker, RQTX, and encrypted protobuf. Cookie length is carried in the low four bits. The pinned binary checks `cookieLen == 15` before copying cookie bytes (`0x7d26e2..0x7d2705`), as does the historical reference. This reconstruction accepts and writes the safe bounded range `0..15` because a real QR response supplied to the integration had a 6-byte cookie; this is an explicit compatibility extension, not a claim that the pinned binary writes every length.

The pinned iPad response decoder at `main+0x7d3200` is intentionally less symmetric. It reads `BF` when present, header length, cookie length, skips the next four bytes, reads UIN, skips one CGI varint and one length varint, and passes bytes starting at `headerLen` to `decryptoIOS`. It does not consume or validate the request marker/RQTX suffix, nor does it compare the skipped four bytes with the request client version. `internal/hybrid.Decode` now follows that response boundary and its synthetic peer uses an independently built response header without request-only marker/RQTX fields.

`PacketCryptType`, `Flag`, `RetCode`, and `UICrypt` are fields used by the historical **Android** `HybridEcdhPackAndroidUn` decoder. Static inspection of the iPad `HybridEcdhPackIosUn` function shows the iPad layout above instead. The meaning of the skipped four response bytes and all currently unconsumed response-header fields remain unverified.

## Boundaries and unknowns

This is a reconstruction informed by a historical public source tree and static evidence; it is not claimed to be clean-room and is not a claim that current WeChat servers will accept the packets. The exact state continuity rules across a full login flow, optional `randomkeyextenddata`, response fields outside the tested subset, the meaning of skipped response bytes, and live CGI behavior remain unverified. Tests use a synthetic local P-256 peer and prove only layer-to-layer encryption, authenticated decryption, truncation/tag rejection, response-boundary handling, and single-use exchange handling.
