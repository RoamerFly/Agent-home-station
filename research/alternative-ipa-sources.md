# Public third-party iOS/iPad IPA route — result

Scope: anonymous, public third-party sources were searched for a **recent WeChat IPA suitable for static comparison** (target range 8.0.75–8.0.80). No Apple ID, shared account, paid/claimed access, package download, or package execution was used.

## Outcome

A recent public **static-research lead** was found in the 22sb public JSON feed. It is explicitly a modified/injected “official-replacement” package, so it must never be called an official or clean IPA. It is nevertheless in scope as a labelled sample from which only the bundled `Info.plist` and main Mach-O could be read offline if the bytes become retrievable.

The public source URL was then verified with a normal GET, `Range: bytes=0-4095`, and normal redirect following. The final response was `206 Partial Content`, `Content-Range: bytes 0-4095/523144542`, `Content-Type: application/octet-stream`; the first bytes were `50 4b 03 04` (`PK\x03\x04`), matching a ZIP-family IPA container. Only 3,285 response bytes were read before the stream was closed. The complete 522 MiB object was not downloaded and no local hash exists yet.

The earlier HEAD-only redirect result is not the transfer verdict: the object-store signature accepted GET but not the HEAD method in this check. No account, payment, signature editing, or signing bypass was used.

### 22sb candidate

- Feed: `http://app.22sb.cn/appstore` — HTTP 200, JSON payload.
- Label/version: `微信_8.0.78_官替十五合一主题版` / `微信8078`.
- Feed timestamp: `2026-09-29T02:18:04+08:00`.
- Public feed URL: `http://114.66.61.131:7260/d/制定/制定/微信_8.0.78_官替十五合一主题版.ipa?sign=Z1F2S8bhXd9lnHunEwS3AV0r3tdTQG_s_wdlYhEBF44=:0`.
- Declared size in feed: `522,190,848` bytes. The accepted GET's `Content-Range` total is `523,144,542` bytes; retain both values as conflicting source metadata until a complete download can be hashed.
- Transfer probe: normal source GET plus `Range: bytes=0-4095` followed redirects and returned `206 Partial Content`; 3,285 bytes read; first four bytes `PK\x03\x04`.
- Modification status: feed describes fifteen injected themes/plugins. It is only a static sample candidate, not Tencent's unmodified application.

## Checked public routes

| Route | Exact URL | Observed status | Result |
| --- | --- | --- | --- |
| Version-history metadata | [SharkApp WeChat history](https://api.sharklatan.com/apple/app-version/US/414478124) | Public page; lists 8.0.75, 8.0.76, 8.0.78 and 8.0.79 version IDs | Metadata only; no anonymous IPA or hash exposed. |
| Maintainer GitHub IPA repository | [OwO-Network/Repo index](https://raw.githubusercontent.com/OwO-Network/Repo/main/index.html) | HTTP 200, 158,162 bytes for the machine-readable index | The newest listed WeChat is 8.0.62 (357,967,048 bytes, dated 2025-08-11); its own metadata says it is injected with named plugins. Not current and not an original/decrypted candidate. |
| Third-party “pure” listing | [Daole popular listing](https://daole.net/popular?page=13) | HTTP 200, 101,086 bytes | The listing labels a WeChat 8.0.78 package “pure”, but its visible description also says multiple WeChat plugins are injected and gives a purchase contact. No anonymous direct IPA URL, size, or hash was exposed. Not actionable from this listing. |
| General IPA archive | [iPA Library WeChat clone](https://ipalibrary.me/wechat-2-wechat-clone/) | Public listing | Only up to 8.0.62, labelled clone; not current and not an original/decrypted candidate. |

## Verification boundary

An injected, re-signed, cloned, or merely version-labelled IPA must not be described as Tencent’s original IPA. It can be a separately labelled offline static sample after exact bytes and a local hash are captured, but it cannot by itself establish a production iPad protocol profile.

The next step for the 22sb candidate is limited to a bounded download of the **public feed-provided URL** for offline parsing, then calculating SHA-256. Do not infer, enumerate, extend, or manufacture a signing token.
