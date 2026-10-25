# iPad protocol profile upgrade: evidence boundary

## Later experiment, 2026-10-24

The initial negative search below has been superseded in one bounded respect:
a different public artifact, PadPro v2.01, exposes ClientVersion 402669099
(0x18003e2b). Its symbols and consumers are retained in
[padpro-upgrade/version-profile.json](evidence/padpro-upgrade/version-profile.json).
This supports an explicitly labelled version-only compatibility experiment,
not a full tuple transplant. The source implementation now preserves the
selected version through fingerprint, QR, poll, authorization and session output.

The experiment kept the established device identity, old device type/bootstrap,
and v8 proof implementation; the different candidate v3 SAE table was not used.
A new fingerprint returned ret=0 and QR5 reached confirmed status 2, but the
single authorization still returned -106 with the application-upgrade message.
See [preflight](evidence/upgrade-preflight-20261024.json) and
[authorization](evidence/upgrade-authorization-20261024.json). This candidate did
not repair account login. The earlier decision is historical search context,
not a claim that no candidate version has since been found.

## Decision

There is **no verified replacement `ClientVersion`** to put into the current
implementation.  In particular, `8.0.80 -> 0x180050xx` is an unsupported
guess and must not be deployed.  The verified old field is `402668320`, or
`0x18003b20`; `0x18003920` was a calculation error (that literal is decimal
`402667808`).  The verified value is only a protocol field from the old ELF;
this investigation did not find a source that proves it is the encoding of public App Store version `8.0.57`.

That distinction matters: the service has explicitly rejected the real,
complete authorization with `ret=-106` and a version-too-old message, but that
does not reveal the next accepted protocol tuple. The message identifies an
outdated application/profile condition; it does **not** isolate the numeric
version as the sole failing field. Device proof, device/profile compatibility,
or other fields coupled to that profile may still be relevant.

## Evidence ledger

| Finding | Evidence | Confidence / boundary |
| --- | --- | --- |
| The old profile contains `ClientVersion=402668320` (`0x18003b20`) and device type `iPad Air iPadOS18.4.1`. | [`evidence/login-static-profile.json`](evidence/login-static-profile.json), [`evidence/version-runtime-audit.json`](evidence/version-runtime-audit.json) | Direct static observation from ELF-backed globals.  It is not an App Store version mapping. |
| A complete authorization was refused as application version too old. | [`evidence/login-fullproof-rejection-20261024.json`](evidence/login-fullproof-rejection-20261024.json) | Direct server response from a test authorization; no account session or message was created. |
| Apple currently exposes WeChat `8.0.80`, dated 2026-10-23 UTC, for CN storefront lookup id `414478124`. | [Apple iTunes Lookup API](https://itunes.apple.com/lookup?id=414478124&country=cn) | Direct public app metadata only.  It contains no internal protocol `ClientVersion`, bootstrap public key, or device profile. |
| The anonymously reachable `houhou/wechat-ipad:latest` registry manifest changed and has tag `v3.2.3`. | [`evidence/version-registry-check.json`](evidence/version-registry-check.json) | Metadata shows a new image, not that its protocol was upgraded. |
| Static extraction of that new image's `usr/wic-go/main` finds the same iPad `ClientVersion=402668320`, device type, and bootstrap key, despite 7,412,208 differing bytes. | [`evidence/version-update/extraction.json`](evidence/version-update/extraction.json), [`evidence/version-update/profile-comparison.json`](evidence/version-update/profile-comparison.json), [`evidence/version-update/byte-diff.json`](evidence/version-update/byte-diff.json) | Direct offline comparison of the relevant values. `static_profile_equal` in the comparison file is currently false only because it also includes a changed init-function hash; do not interpret that boolean as a profile-field difference. |
| The only inspected source checkout is historical: its latest commit is 2024-05-23 and declares `0x18001621` plus older commented literals. | [`evidence/version-public-source-scan.json`](evidence/version-public-source-scan.json) | Shows versions changed historically, but establishes no numeric encoding rule and is too old for current use. |

## What the version number does and does not prove

The data support this narrow statement:

```
old ELF profile -> ClientVersion 0x18003b20 as one component of an old profile
                -> server says the application/profile is too old
```

They do **not** support either of these transformations:

```
App Store 8.0.80 -> protocol ClientVersion ?
protocol ClientVersion 0x18003b20 -> App Store 8.0.57 ?
```

The inspected historical source has successive literals such as `0x18000720`
and `0x18001621`; that demonstrates version-like fields change, not how every
public semantic version is encoded.  Its low byte cannot be assumed to be an
App Store patch number or build number.

## New ELF: default versus runtime copy

The current image's `.noptrdata` contains bytes `20 3b 00 18` at virtual
address `0x1aa9c58`, which is little-endian `0x18003b20` / `402668320`.
`Algorithm.(*Client).Init` and `Init2` both select exactly that slot for the
`"iPad"` branch (`0x7d16ca` and `0x7d20ea`). A complete `.text` objdump scan
found reads and comparisons of the slot but no direct RIP-relative store or
address-take. Thus this image has no statically visible initialization or
configuration path that mutates the iPad global itself.

There is a separate, more limited runtime fact: `Login.GenIpadLoginData`
keeps a **positive caller-provided version**, only using the iPad global when
its supplied value is zero; it then copies the choice to `LoginData+0xa0`.
`UpdateIpadLoginData` likewise fills that field only if zero. `GetQRCODE`
compares an existing field with the iPad global and invokes the generate path
when they differ. This proves a local per-object override path, but says
nothing about server acceptance and does not yield a valid replacement number.
The exact addresses and scope limits are retained in
[`evidence/version-runtime-audit.json`](evidence/version-runtime-audit.json).

The same selector has a distinct iPhone slot (`0x18003b26`) and platform-
specific Android, desktop, and car values. Those are **not** newer iPad
profiles: each branch also selects its own surrounding profile material, so a
different platform constant must not be transplanted into iPad.

The new-image comparison further shows that the iPad default version, device
type, and bootstrap key remain identical to the old ELF. Its named-function
inventory is also the same, with only `wechatdll/models/FriendCircle.Messages`
growing by 224 bytes. Therefore the changed image supplies neither a newer
iPad profile nor evidence that a protocol upgrade has been implemented.

## Required coherent upgrade unit

An acceptable candidate must be recovered from **one matching, newer artifact
or source release**, then cross-checked before any test authorization.  It is
not safe to change just the numeric version.  The minimum grouped fields are:

1. `ClientVersion` wherever the login/base request and transport header carry
   it.
2. The matching device identity tuple: device type, model/name, OS version and
   any profile literals consumed by the device proof.
3. The matching bootstrap ECDH public key and the initialization path that
   selects it.
4. The companion `08sae.dat` / `rqtx.dat` data and code assumptions used by
   the device-proof and request path, if the artifact changes them together.
5. Any changed request schemas, hybrid/MMTLS framing, and post-login send
   behavior from that same artifact.

The registry image currently available fails item 1: its extracted constant is
unchanged.  Its large byte diff is a reason to inspect dependencies as a set,
not a reason to borrow individual bytes or constants.

## Practical next gate

Do not submit another login using a guessed number.  First locate an auditable
newer artifact/source whose static fields demonstrably differ from the old
profile, retain its digest and extraction evidence, and compare the full tuple
above.  Only then is a single, bounded test authorization justified; record
only redacted result metadata.  Until that source exists, the protocol upgrade
remains unresolved rather than "fixed by version bump".

## Public-source search boundary

The exact searches and their limitations are recorded in
[`evidence/version-public-source-scan.json`](evidence/version-public-source-scan.json).
Anonymous GitHub code search was sign-in-gated and the web index yielded no
exact results for `402668320`, `0x18003b20`, `iPad Air iPadOS18.4.1`, or
`8.0.80 ClientVersion`.  This does not prove that an upstream exists nowhere;
it prevents treating an unverified marketing claim or an App Store label as a
protocol profile.
