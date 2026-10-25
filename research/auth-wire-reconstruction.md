# 授权阶段 protobuf 与 P-224 边界

`internal/authwire` 是独立、标准库实现，覆盖固定样本嵌入 `mm.proto` 描述符中的
`SecManualLoginRequest`、`ManualAuthRsaReqData`、`ManualAuthAesReqData` 和
`UnifyAuthResponse` 最小子集。它不链接闭源二进制、不发起网络连接，也不会格式化输出
临时 password 或会话密钥。

字段编号来自 `research/evidence/send-mm-descriptor.bin`（JSON 展开在同目录）。
历史公开参考仅用于验证结构层次和 P-224 表示方式；没有复制其源代码。

## 建立授权请求

```go
func BuildRequest(in authwire.RequestInput) ([]byte, error)
```

该函数输出 `SecManualLoginRequest`：

- 字段 1：`ManualAuthRsaReqData`，包含 random key(1)、NID=713 的客户端 P-224
  公钥(2)、扫码通知返回的 username(3)、临时 password(4, 5)。
- 字段 2：`ManualAuthAesReqData`，包含 `BaseRequest`(1)、按调用方明确提供的
  `BaseAuthReqInfo`(2) 与设备字段：IMEI(3)、softType(4)、builtinIpseq(5)、
  clientSeqId(6)、signature(7)、deviceName(8)、deviceType(9)、language(10)、
  timeZone(11)、channel(13)、timestamp(14)、deviceBrand(15)、deviceModel(16)、
  osType(17)、realCountry(18)、bundleId(19)、adSource(20)、iPhoneVer(21)、
  inputType(22)、clientCheckData(23)、extSpamInfo(24)。

`ExtSpamInfo` 和 `ClientCheckData` 只能由调用者提供真实的已恢复设备证明；空缺时保持
protobuf 字段不存在。包不会伪造设备令牌、spam 证明或签名。`Secret` 实现的普通
`String()` 固定输出 `[redacted]`。

## 解析授权响应

```go
func ParseResponse(raw []byte) (authwire.AuthorizationResponse, error)
```

它保留所有已读字段的 presence，读取：BaseResponse、section flag、UIN、服务端
ECDH key、加密 session key、auto-auth key、client/server session key、账户基本
资料和 `NetworkSectResp.newHostList` 的 host/redirect 候选。`Code()` 要求
`BaseResponse.ret` 实际出现，缺失时返回 `ErrMissing`，不会默认为成功。

## P-224 与会话解密

```go
func GenerateP224() (private, public []byte, err error)
func AuthorizationAESKey(private, serverPublic []byte) ([]byte, error)
func DecryptSessionKey(private, serverPublic, ciphertext []byte) ([]byte, error)
```

公钥使用 SEC1 非压缩格式 `0x04 || X || Y`，P-224 时长度为 57 字节。实现会验证
私钥范围、点解码和 `IsOnCurve`。

这里有两个不能混同的 shared-X 表示：`big.Int.Bytes()` 是可变长，会丢弃前导零；
`SharedXPadded` 固定为 P-224 的 28 个大端字节。公开历史参考自身存在两条不同路径：
`DoECDH713` 左补零至 `(224+7)/8=28` 后 MD5；而 `CheckSecManualAuth` 调用
`DoECDH713Key`，后者直接对 `x.Bytes()` 的无前导零值 MD5。二者只在 shared-X 恰有
前导零时不同，却会得到不兼容的 AES key。

因此本包保留固定宽度默认 `AuthorizationAESKey` / `DecryptSessionKey`，并提供
`AuthorizationAESKeyLegacyUnpadded` / `DecryptSessionKeyLegacyUnpadded` 作为历史
CheckSecManualAuth 候选。两者都使用 AES-128-CBC `key=IV=MD5(shared-X表示)`，严格
验证密文块长度和所有 PKCS#7 padding 字节。调用方应仅以同一真实授权响应可成功解开
且结果满足后续结构校验作为选择依据，不能根据静态名称自动切换。

当前固定样本的 Ghidra 导出确认存在 `Login.SecManualAuth` 入口，但没有取得足以判定
它最终采用哪条 shared-X 表示的完整高可信数据流。因此这两种模式均属候选；尚未通过
当前闭源样本的真实授权响应逐字验证，也不证明服务端将接受请求。

## 验证边界

2026-10-24 补充：`BuildExtInfoV8` 已按当前 ELF 的 struct 布局和 `SecManualAuth`
数据流实现 `CryptoData` / `DeviceToken` / `WCExtInfo` 容器编码，具体字段映射见
`auth-field-audit-20261024.md` 和 `evidence/login-go-struct-layouts.json`。后者的 13 个
message、77 个 protobuf 字段名及编号均与另行提取的描述符一致。容器包自身只包装
producer 输出；随后 `internal/deviceproof` 已实现 `GetWcstf08` / `GetWcste08` /
`GetNewSpamDataV8` 对应链，`loginflow.AuthorizationProof` 负责组合。`fpinitnl`
已真实取得服务器签发的设备令牌，见 `evidence/fingerprint-live-20261024.json`。
这批附加信息尚未完成账号授权验收；新 QR 的后续授权仍需手机扫码确认。

`make_extinfo_fixture.py` 使用独立 Python protobuf 生成纯 synthetic 编码对照，验证
了容器层次、tag，以及 proto2 显式零值/空字符串的 presence；不含真实设备令牌，
也没有执行原样本的 marshaler。该测试不能证明微信接受 proof。

`internal/authwire/auth_test.go` 使用纯 synthetic protobuf 和本地生成的 P-224
密钥对，覆盖请求字段层次、NID=713、响应 presence、host 候选、双方共享密钥、
AES-CBC 解密及 padding 篡改失败。fixture 不含登录态、密码、网络流量或真实服务端
响应。解析限制为 2 MiB、256 字段、12 层嵌套，拒绝非法 varint、长度、UTF-8、
legacy group 和 `SKBuiltinBuffer_t.iLen` 不匹配。
