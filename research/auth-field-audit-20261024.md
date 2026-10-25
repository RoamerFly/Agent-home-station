# `SecManualAuth` 字段审计（2026-10-24）

## 范围和结论

本报告只读取工作区源码、已保存的 ELF 静态导出、镜像元数据和 protobuf 描述符。没有读取 `secrets/`，没有向微信或第三方发请求，也没有运行原 `main`、`libv08.so` 或其它闭源程序。

当前独立实现已得到二维码、轮询到状态 `2`，但一次 `secmanualauth` 得到服务端业务返回 `-106`。这证明服务端已处理并返回了该 CGI 的业务结果；**不能**据此证明哪个字段缺失、也不能把它解释为可通过枚举字段或伪造证明绕过的错误。

最明确的当前差异是：本次授权调用没有实际设备证明，所以 `ManualAuthAesReqData.clientCheckData`（字段 23）和 `extSpamInfo`（字段 24）都不存在。其次是当前源码自行拼接了一组没有静态来源的设备画像字段。当前 ELF 的 `Login.SecManualAuth` 静态导出确实构造了请求、使用 CGI `0xfc`（十进制 252）并经 MMTLS POST；现有导出**没有**给出一个可命名为 `SaeEncrypt07`、`fpinitnl` 或 `WCExtInfo` 的直接被调函数，因此不能称已证明它调用了其中任一个。

## 当前实现实际发送的字段

`internal/loginflow/authorize.go` 通过 `authwire.BuildRequest` 构造两层 protobuf：

| 层/字段 | 当前值或 presence | 审计结论 |
|---|---|---|
| `ManualAuthRsaReqData` 1 | 16 字节新随机 key | 已实现；尚无本版逐字节样本对比 |
| `ManualAuthRsaReqData` 2 | 新 P-224、NID 713 公钥 | 已实现；描述符和历史流程都支持该层次 |
| `ManualAuthRsaReqData` 3/4/5 | QR notify 的 username、temporary password、同一 password | 已实现；不在日志输出 |
| `ManualAuthAesReqData.baseRequest` 1 | sessionKey=random key，UIN=0，持久 DeviceID，版本，设备类型，scene=1 | 版本及 `BaseRequest.deviceType` 有固定 ELF 画像依据；其它值尚无本版字节样本 |
| `baseReqInfo` 2 | 一个空子消息 | 字段确实存在，但其内部 `wtloginReqBuff`、`authReqFlag`、`authTicket` 等全未填；未知是否必须 |
| 设备字段 3/6/8/9/10/11/13/14/15/17/18/19/21/5/22 | 当前代码提供 IMEI、clientSeqId、名字/类型、语言/时区、channel/timestamp、品牌、OS、国家、bundle、iPhoneVer、builtinIpseq、inputType | 其中多项是实现选择，未证明与镜像一致 |
| `softType` 4、`signature` 7、`deviceModel` 16、`adSource` 20 | 缺席 | 描述符允许，但尚无本版证据证明这些字段单独必需 |
| `clientCheckData` 23 | 缺席 | 高优先级缺口；没有 CLI 输入或生产器 |
| `extSpamInfo` 24 | 缺席（只有显式 `--device-proof` 才能填入） | 高优先级缺口；本次未提供真实 proof |

代码锚点：`internal/loginflow/authorize.go:58-88`、`internal/authwire/auth.go:98-135`、`cmd/wechat-login/authorize.go:22-71`。字段号/类型来自 `research/evidence/send-mm-descriptor.json:1226-1567`。

## 可证实的设备证明缺口

`AuthorizationOptions` 可以承接两个原始字节串，但调用方没有生成它们。CLI 只有 `--device-proof`，且只赋给 `ExtSpamInfo`；它没有 `ClientCheckData` 的输入路径。这里的设计避免了凭空伪造 proof，但也意味着本次真实请求的 23/24 字段都没有出现。

描述符中的 `WCExtInfo` 有 `wcstf`、`wcste`、`ccData`、`userAttrInfo`、`acgiDeviceInfo`、`acgiTuring`、`deviceToken`、`iosturingHuman`、`iosturingOwner` 字段，见 `research/evidence/send-mm-descriptor.json:2819-2891`。这证明该版本 schema 支持设备/风控数据的结构；它**不**证明 `WCExtInfo` 必须放进字段 24，也不证明每个字段在当前账户或当前服务端策略下都必需。

镜像元数据列出 `08sae.dat`，见 `research/evidence/image-config.json`。这个资源的存在以及历史参考中的 iPad token 路径，足以把 SAE/token 恢复列为后续研究目标；不能由此推断 `SaeEncrypt07` 是当前 `SecManualAuth` 的直接调用。

## 当前 ELF 的直接静态调用证据

以下地址来自固定 ELF（SHA-256 记录于 `research/evidence/login-static-profile.json`）的 Ghidra 导出；均为静态观察，不是运行时跟踪。

| 观察 | 固定地址/调用点 | 能证明什么 | 不能证明什么 |
|---|---|---|---|
| 扫码完成转入检查 | `Login.CheckUuid` `0x00b77f40` 在 `0x00b787f1` 调 `0x00b76120` | 状态 2 后有单独的 CheckSecManualAuth 阶段 | 该阶段内每个 protobuf 字段值 |
| 授权请求构造 | `Login.SecManualAuth` `0x00ba0f80` | 这是本版单独的请求构造函数 | Ghidra 伪代码中匿名 Go 辅助函数的语义名称 |
| 业务 CGI 值 | `0x00ba25ad`: `func_0x007d2540(..., 0xfc, ...)` | 本版请求设置十进制 252 | 这个参数在全部包装层中的完整含义 |
| transport 调用 | `0x00ba2640 -> 0x008f3060`，前者传入会话对象字段 `+0x118/+0x120` | 当前 SecManualAuth 经同一 MMTLS POST 入口发送 | Hybrid 包装的每一个头字段或 cookie presence |
| 已知直接 helper | `0x00ba139a -> 0x007cc3a0`、`0x00ba13b7 -> 0x007cc820`、`0x00ba13e0 -> 0x00b40ee0`，以及多处 `0x006ce020` | 授权在本版确实有多个数据构造/序列化 helper | 这些匿名地址分别等价于 SAE、fpinitnl、WCExtInfo 或设备 token；现有导出没有这个名称/数据流 |

来源：`research/evidence/login-ghidra.txt:1319,1367,1397-1398,2318-2326,2469-2500` 和 `research/evidence/main-selected-disassembly.txt:6701-6707`。在当前 `Login.SecManualAuth` 的完整已列 callee 表中，未出现已命名的 `fpinitnl`、`SaeEncrypt07`、`WCExtInfo` 或 device-token 函数。

相反，`Login.GetQRCODE` 的当前静态 call list 显示它在 `0x00b7dd54`、`0x00b7dda9`、`0x00b7ddef`、`0x00b7de30` 调用了四个未命名 helper（目标分别为 `0x00b88660`、`0x00b88ca0`、`0x00b892e0`、`0x00b88020`），并在 `0x00b7dc36` 调 `Login.GenIpadLoginData` (`0x00b98c80`)。这是当前版本存在设备相关前置链的证据，但在没有对这些 helper 的单独静态数据流恢复前，不能把它们命名为历史 `IPadGetDeviceToken` 或 `SaeEncrypt07`。

## 当前画像的未证实部分

固定 ELF 画像仅支持：`clientVersion=402668320`，以及 `BaseRequest.deviceType="iPad Air iPadOS18.4.1"`，见 `research/evidence/login-static-profile.json`。当前 `authorize.go` 另外自主选择：

- `DeviceName` / `DeviceType` 为 `iPad`；
- `IPhoneVersion` 为 `iPad13,1`；
- `IMEI=hex(DeviceID)`、`ClientSeqID=IMEI-<current unix time>`；
- `TimeZone="8.0"`、`Channel=0`、`InputType=2`、`Brand="Apple"`、`Country="CN"`、`BundleID="com.tencent.xin"`。

这些值可使 protobuf 成形，但没有固定 ELF 字段级来源，不能作为“镜像还原”陈述。它们与缺失 proof 一起，是解释 `-106` 时最应避免继续猜测的部分。

## 与历史 iPad 参考的边界

历史 iPad 参考被既有研究文档用于说明“token/设备证明可能存在”和 ECDH 的候选表示，不是当前版本执行证据。`research/login-reconstruction-plan.md:25-29,56-58` 明确把 `fpinitnl` 标为待恢复的设备 token 路径；`research/auth-wire-reconstruction.md:43-55` 明确记录 fixed-width 与 unpadded shared-X 两种候选。后者只会影响成功授权响应的 session-key 解密，不能解释已经返回业务 `-106` 的本次拒绝。

## 下一轮离线恢复顺序

1. 对 `0x00ba0f80` 做定点静态数据流：将 `0x006ce020` 的 proto marshal 输入、字符串地址和对象布局映射回 `ManualAuthAesReqData` 字段 presence，产出镜像字段表，而非用返回码猜测。
2. 单独分析 `0x00b88660`、`0x00b88ca0`、`0x00b892e0`、`0x00b88020` 与 `0x00b98c80` 的调用链、输入输出以及 `08sae.dat` 的只读解析，确认它们是否产生设备 token/证明。
3. 在得到真实生成链之前，保留 23/24 为 absent；新增接口时只接受该链实际生成的字节，记录 length/presence 而不记录内容。
4. 得到可验证的真实设备资料/token 后，再用新的 QR 流程进行一次受控登录验证。禁止靠随机 proof、字段组合枚举或重复提交 `-106` 来探测风控策略。

## 未解决问题

- `0x00b40ee0`、`0x007cc3a0`、`0x007cc820` 的语义尚未恢复。
- 当前 SecManualAuth 是否携带 Hybrid cookie，以及 cookie 与轮询返回 cookie 的关系尚未建立。
- `WCExtInfo`、`ClientCheckData`、`ExtSpamInfo` 的容器关系和精确字节生产器尚未恢复。
- 未取得当前版本成功的 `UnifyAuthResponse`，因而无法验证 session-key shared-X 表示，也不能宣称发送会话已创建。
- `newinit` / 同步是否是首条文本前的必需步骤仍未得到当前版本互通证据。

## 追加：Go 结构布局映射（2026-10-24）

`research/evidence/login-go-struct-layouts.json` 新增了固定 ELF 的 Go 1.24 `abi.StructType` 静态解析。它把此前 Ghidra 中的 `UNK_*` 分配类型映射为真实 protobuf struct；本节据此重新解释 `Login.SecManualAuth` 的写偏移。该解析仍然没有执行样本。

### 已解析的 helper 名称

对 `research/evidence/main-functions.json` 做内存 JSON 查询得到以下精确名称（没有对单行巨型 JSON 使用文本搜索）：

| 静态地址 | 当前 ELF 名称 | 在 `SecManualAuth` 中的作用 |
|---|---|---|
| `0x007cc3a0` | `wechatdll/Algorithm.GetWcstf08` | 产生 `WCExtInfo.wcstf` 的字节 slice |
| `0x007cc820` | `wechatdll/Algorithm.GetWcste08` | 产生 `WCExtInfo.wcste` 的字节 slice |
| `0x00b40ee0` | `wechatdll/clientsdk/extinfo.GetNewSpamDataV8` | 产生写入 `CryptoData.encryptData` 的字节 slice |
| `0x006ce020` | `github.com/golang/protobuf/proto.marshalAppend` | 顺序序列化 `CryptoData`、`DeviceToken`、`WCExtInfo` 和最终请求 |
| `0x007d2540` | `wechatdll/Algorithm.(*Client).HybridEcdhPackIosEn` | 为 CGI 252 封装最终 protobuf |
| `0x008f3060` | `wechatdll/Mmtls.(*HttpClientModel).MMtlsPost` | 发送已经封装的请求 |

这已经比历史 iPad 参考强：当前样本**直接**调用 `GetWcstf08`、`GetWcste08` 和 `GetNewSpamDataV8`。它仍不能证明这些名称与其它版本里的 `SaeEncrypt07` 有相同实现或输入格式。

### `CryptoData`：当前样本明确构造并嵌入

类型 `mm.CryptoData` 位于 `0xef51a0`，其字段布局见新证据。`SecManualAuth` 在约 `0x00ba1440..0x00ba15a0` 构造它：

| `CryptoData` 字段 | protobuf tag | 当前静态写入 | 证据/限制 |
|---|---:|---|---|
| `Version` | 1 | ASCII `"00000008"`（8 bytes） | `0x3830303030303030` 和 slice 长度/容量 8；已确定 |
| `Type` | 2 | 指针值 `1` | 已确定 |
| `EncryptData` | 3 | `GetNewSpamDataV8(state)` 的返回 slice | helper 名称和数据流已确定；该 helper 内部原始输入尚未恢复 |
| `Timestamp` | 4 | 由 `0x004eecc0` 返回值经算术计算的当前时间样式值 | 写入发生已确定；具体 epoch/单位尚待核对 |
| `Unknown5` | 5 | 指针值 `5` | 已确定 |
| `Unknown6` | 6 | 指向新分配、在该片段中未写入的 `uint32`，即可观察为零值指针 | presence 已确定；不能仅从反编译文本断言序列化库会否省略零值 |

该对象由 `proto.marshalAppend` 序列化，结果随后成为 `WCExtInfo.ccData`，不是直接塞入顶层 AES 请求。

### `DeviceToken`：当前样本明确封入 `WCExtInfo`

类型 `mm.DeviceToken` 位于 `0xef52e0`。在约 `0x00ba15e0..0x00ba1805`，当前样本构造并序列化它：

| `DeviceToken` 字段 | protobuf tag | 当前静态写入 | 已知输入 / 未知 |
|---|---:|---|---|
| `Version` | 1 | 指向新分配的零值 string header | 非空指针 presence 可见；其最终 wire presence 仍需 marshal 行为核对 |
| `Encrypted` | 2 | `1` | 已确定 |
| `Data` | 3 | `SKBuiltinStringT.String_` 指向登录 state 的嵌套字段链 `state+0x278 -> +0x30 -> +0x30` | 当前函数复用已存 token 文本，没有在这里生成 token；该 state 字段的生产者未恢复 |
| `TimeStamp` | 4 | 同一类由 `0x004eecc0` 派生的时间值 | 单位未知 |
| `Optype` | 5 | `2` | 已确定 |
| `Uin` | 6 | 指向新分配、未在此片段写入的 `uint32` 零值 | presence 可见；wire 行为待核对 |

`DeviceToken` 的 marshal 结果随后成为 `WCExtInfo.deviceToken`。这修正了之前“仅推测 WCExtInfo 可能含 token”的表述：当前固定样本的 `SecManualAuth` 确实构造并嵌入了一个 `DeviceToken`，只是 token 本体来自尚未恢复的持久 state 字段。

### `WCExtInfo` 与 `ManualAuthAesReqData.extSpamInfo` 的确定绑定

类型 `mm.WCExtInfo` 位于 `0xf0e3e0`，其构造范围约为 `0x00ba1843..0x00ba1af3`。当前样本写入四个字段：

| `WCExtInfo` 字段 | tag | 当前来源 |
|---|---:|---|
| `Wcstf` | 1 | `GetWcstf08` 的 slice，调用参数来自当前 login state 的 `+0x8/+0x10` |
| `Wcste` | 2 | `GetWcste08` 的 slice |
| `CcData` | 3 | 上表 `CryptoData` 的 `proto.marshalAppend` 结果 |
| `DeviceToken` | 7 | 上表 `DeviceToken` 的 `proto.marshalAppend` 结果 |
| `UserAttrInfo`、`AcgiDeviceInfo`、`AcgiTuring`、`IosturingHuman`、`IosturingOwner` | 4/5/6/101/102 | 在这个构造片段没有写入，故为 absent |

在约 `0x00ba248d..0x00ba256f`，`ManualAuthAesReqData` (`0xf38c00`) 的偏移 `+0xd8` 被写入一个 `SKBuiltinBufferT`，该 buffer 是前述 `WCExtInfo` 的 marshal 结果。根据布局，`+0xd8` 正是 **`ExtSpamInfo` / protobuf 字段 24**。因此可静态确认：

```text
GetWcstf08 / GetWcste08 / GetNewSpamDataV8
   → CryptoData + DeviceToken
   → WCExtInfo
   → ManualAuthAesReqData.extSpamInfo (tag 24)
```

同一 `ManualAuthAesReqData` 的 `+0xd0` 是 `ClientCheckData` / tag 23。该函数的字段写入中没有给 `+0xd0` 赋值，故当前样本的这条路径将 **23 保持 absent**。这也意味着我们此前构造“可选 23、24 都为空”的请求，至少与样本在 23 的 absence 相符，却确定地缺少样本构造出的 24。

### 与当前独立实现的直接差异

独立实现 `internal/loginflow/authorize.go:74-80` 目前只有调用者提供 bytes 时才写入 tag 23/24；一次未提供 proof 的真实尝试使 23、24 都 absent。当前样本已证明 tag 24 并非泛称的任意 proof，而是上述 version-8 `WCExtInfo` 的 marshal 包装。因此下一步应恢复其真实、当前版本的 state/token 与三个 helper 输入输出，再实现这一确定的数据结构；不能以任意文件内容填充 `--device-proof`。

### 仍未解决的输入和验证边界

- `GetNewSpamDataV8`、`GetWcstf08`、`GetWcste08` 的内部算法、`08sae.dat` 使用方式以及 `SaeEncrypt07` 的同/异版本关系尚未分析；不得把函数名相似视为等价。
- `state+0x278 -> +0x30 -> +0x30` 的 `DeviceToken.Data` 生产者、刷新时机与失效策略未知。它需要从 `FpInitAndRrefresh*` 和 `UpdateIpadLoginData` 的静态数据流恢复。
- `Version`/`Uin` 的零值指针是否在这个具体 Go protobuf marshaler 中落到 wire 需要小型离线 marshal 对照，不应靠 Ghidra 文本猜测。
- 以上证明仅解释当前样本怎样填 tag 24；它不证明复制该格式会改变服务端 `-106`，也不授权伪造 token 或反复提交测试。

## 追加：`GetWcstf08` / `GetWcste08` 的 v8 包装（2026-10-24）

本节只分析新导出的 `research/evidence/device-v8-ghidra.txt` 与固定 ELF 的只读数据。
函数名来自对 `main-functions.json` 的内存 JSON 查询；没有启动 Ghidra、执行样本或发起任何网络请求。

### 共同的二层 protobuf + 编码包装

两函数的返回值都不是内层 `WCSTF` / `WCSTE` 的原始 protobuf，而是下列外层对象的 protobuf：

```text
inner WCSTF or WCSTE
  -> github.com/golang/protobuf/proto.marshalAppend
  -> wechatdll/clientsdk/v08.Compress (0x007bdd00)
  -> wechatdll/clientsdk/ccdata.EncodeZipData (0x007c9bc0)
  -> wechat.DeviceRunningInfoNew
  -> github.com/golang/protobuf/proto.marshalAppend
```

共同外层 `wechat.DeviceRunningInfoNew`（ABI type `0xef7360`）的实际写入为：

| 字段 | tag | 确定值 / 来源 |
|---|---:|---|
| `Version` | 1 | ASCII `"00000008"`（8 bytes） |
| `Type` | 2 | `1` |
| `EncryptData` | 3 | `EncodeZipData(Compress(marshal(inner)))` 的返回 bytes |
| `Timestamp` | 4 | 每个函数各自取得的当前时间值；单位见下表 |
| `Unknown5` | 5 | `5` |
| `Unknown6` | 6 | 新分配但未写入的 `uint32` 零值指针；是否落到 wire 仍取决于具体 marshaler 的零值/presence 规则 |

因此 `WCExtInfo.wcstf` 和 `WCExtInfo.wcste` 需要的分别是这个**外层**序列化结果。`Compress` 和 `EncodeZipData` 的字节格式尚未在本节恢复，不能以普通 gzip、任意 zip 或自定义 base64 代替。

### `GetWcstf08`（`0x007cc3a0`）

内层类型是 `wechat.WCSTF`（ABI type `0xedf020`），布局和固定样本的赋值如下：

| 字段 | tag | 静态赋值 |
|---|---:|---|
| `StartTime` | 1 | 调用时取得的当前毫秒样式值 |
| `CheckTime` | 2 | 同一当前毫秒样式值 |
| `Count` | 3 | 第二个调用参数 |
| `EndTime` | 4 | 长度等于 `Count` 的 `uint64` repeated 序列 |

`EndTime` 从起始毫秒值开始，循环调用 `math/rand.Int63n(10000)`（`0x005df740`）并累加；每次累加后的值追加到序列。反编译片段中第一个参数仅被保存，未见参与这个导出函数体的字段计算；不能由此断言其在更早的调用点没有语义。

外层 `Timestamp` 使用该毫秒样式时间值。这里“毫秒样式”只描述当前静态除法/乘法数据流；其准确 epoch 和 Go 到 `uint32` 的截断语义仍需离线核对。

### `GetWcste08`（`0x007cc820`）

内层类型是 `wechat.WCSTE`（ABI type `0xf1efa0`）。固定 ELF 在虚拟地址 `0xf698b5` 的静态字符串为 `"<LoginByID>"`。函数构造：

| 字段 | tag | 静态赋值 |
|---|---:|---|
| `CheckId` | 1 | `"<LoginByID>"` |
| `StartTime` | 2 | 当前秒样式值 |
| `CheckTime` | 3 | 同一当前秒样式值 |
| `Count1` | 4 | 新分配但未写入的 `uint32` 零值指针 |
| `Count2` | 5 | `1` |
| `Count3` | 6 | 新分配但未写入的 `uint32` 零值指针 |
| `Const1` | 7 | `0x0555015050505051` |
| `Const2`..`Const5` | 8..11 | 同一当前毫秒样式值 |
| `Const6` | 12 | `0x0554400000000000` |

外层 `DeviceRunningInfoNew.Timestamp` 则使用该函数的当前秒样式值。tag 4、6 的指针在 wire 中是否出现，和前述 `Unknown6` 一样须以相同版本 protobuf marshaler 的离线对照确认。

### 当前 `GetNewSpamDataV8` 的最短依赖边界

`GetNewSpamDataV8`（`0x00b40ee0`）是 `CryptoData.EncryptData` 的单独输入，不是 `WCSTF/E` 共同包装的一部分。已可从命名映射确认其直接使用的 v8 编码原语为：

| 地址 | 名称 | 恢复时的含义 |
|---|---|---|
| `0x007bd5a0` | `wechatdll/clientsdk/v08.Rqtx` | v8 数据块原语；输出格式尚未恢复 |
| `0x007bd760` | `wechatdll/clientsdk/v08.EncodeString` | 字符串编码原语 |
| `0x007bd9a0` | `wechatdll/clientsdk/v08.EncodeUInt64` | 无符号整数编码原语 |
| `0x007bdd00` | `wechatdll/clientsdk/v08.Compress` | 共同压缩步骤 |
| `0x007c9bc0` | `wechatdll/clientsdk/ccdata.EncodeZipData` | 共同封装步骤 |

形成一次真实 `ExtSpamInfo` 所需的最小离线恢复清单为：

1. 与该 ELF 相同的 protobuf schema 和 marshaler presence 行为；
2. `GetWcstf08` 的时钟转换及 `math/rand.Int63n(10000)` 序列，及 `GetWcste08` 的两种时间值和上述常量；
3. `v08.Compress` 和 `ccdata.EncodeZipData` 的精确字节级实现；
4. `GetNewSpamDataV8` 的 `Rqtx`、字符串/整数编码、它读取的运行时/设备输入及其输出组装；
5. 已有持久 `DeviceToken.Data` 的合法生产、刷新和存储路径。

第 3--5 项仍是阻塞项。尤其不能用新随机 token、伪造设备值或猜测压缩格式替代它们，也不应因这些静态字段而再次提交 `-106` 登录请求。
