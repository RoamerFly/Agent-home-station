# `Newinit@0x00b9f220` 离线重建记录

日期：2026-10-24。范围仅为固定样本的静态反编译、已导出的 Go ABI
结构信息与 protobuf descriptor；没有运行样本、发起网络请求或读取会话/密钥。

## 可确认的请求体

`mm.NewInitRequest` 的 ABI 类型位于 `0xee9c20`（80 字节）。
`research/evidence/newinit-go-layouts.json` 给出了字段偏移和 tag：

| protobuf field | ABI 偏移 | 传入值/来源 |
|---|---:|---|
| `BaseRequest` (1) | `+0x28` | 已授权 `LoginData` 的会话资料构造；`SessionKey`、`Uin`、`DeviceId`、`ClientVersion`、`DeviceType`、`Scene` 对应 `mm.BaseRequest@0xef1e60` 的 1..6 字段 |
| `UserName` (2) | `+0x30` | 登录状态中的用户名；静态控制流使用状态字符串构造此 pointer-backed 字段 |
| `CurrentSynckey` (3) | `+0x38` | `Newinit` 第 1 组形参 byte slice；其 `SKBuiltinBuffer_t.ILen` 为该 slice 长度 |
| `MaxSynckey` (4) | `+0x40` | 第 2 组形参 byte slice，同上 |
| `Language` (5) | `+0x48` | ELF 常量 `zh_CN` |

样本把 `Scene` 指向常数 `3`。两个同步 key 即使为空也以一个
`SKBuiltinBuffer_t@0xeaa660` 指针构造：`ILen`（field 1）和 `Buffer`
（field 2）分别位于 `+0x28`、`+0x30`。因此重建实现保留“字段出现且为
零”的语义，而不是用 Go 零值悄悄抹掉它。

封装层以 `github.com/golang/protobuf/proto.marshalAppend@0x6ce020`
编码，再调用 `comm.SendRequest@0x00b08a80`；响应以
`proto.Unmarshal@0x6ce220` 解码。该层不属于本报告的 protobuf codec。

### 传输模板（已确认）

`comm.SendRequest` 使用的 global template 是 `0x013d7a68`。按与
`sendTemplate` 相同的结构读取：

- `+0x20`：`/cgi-bin/micromsg-bin/newinit`，长度 `0x1d`；
- `+0x78`：原始值 `0x8b`（CGI `139`）；
- `+0xc0`：`5`（加密类型）。

这支持为 CGI 139 走既有 type-5 `EncodeCGI` 路径；本次静态范围未发现
该模板需要 type 13/7 或 zlib 的相反字段。不能由此推断其他 CGI 的格式。
`LoginData.Sessionkey` 既是 BaseRequest field 1 的输入，也是 transport
`Algorithm.Pack@0x7d3e20` 的输入；`LoginData.Cooike` 被拷入 outbound
carrier。`Newinit` 本身没有解析或更新 HTTP `Set-Cookie`，故 cookie 维护
应仍由 transport/session 层负责。

## 已确认的响应体和本地状态变化

`mm.NewInitResponse@0xf06c00` 为 120 字节：

| field | ABI 偏移 | 观察到的用途 |
|---|---:|---|
| `BaseResponse` (1) | `+0x28` | descriptor 为 `ret:int32`、`errMsg:SKBuiltinString_t` |
| `CurrentSynckey` (2) | `+0x30` | 循环后其 `Buffer` slice（`+0x30/+0x38/+0x40`）复制到 `LoginData.SyncKey` |
| `MaxSynckey` (3) | `+0x38` | response schema 证明存在；本函数片段未证明其最终持久化目的 |
| `ContinueFlag` (4) | `+0x40` | presence 应保存；在 `Newinit` 本体仅见成功末尾的结构记录读取，未见以它 branch 或再次调用 NewInit |
| `SelectBitmap` (5) | `+0x48` | presence 应保存；未在此函数中确认发送前用途 |
| `CmdCount` (6) | `+0x50` | 应等于 decoded `CmdList` 数量；安全 codec 拒绝不一致 |
| `CmdList` (7) | `+0x58` | 必须至少一项；每项 `CmdId:int32`、`CmdBuf:SKBuiltinBuffer_t` |
| `Ratio` (8) | `+0x70` | presence 应保存；本函数中未确认发送前用途 |

反编译在 unmarshal 后明确执行 `response.CmdList.data != nil && len != 0`
测试；失败直接返回。它随后无空检查访问 `response.CurrentSynckey.Buffer`
并拷贝到状态。因此可靠重建不应把“CmdList 为空”或
`CurrentSynckey` 缺失解释为成功。原样本在该位置**没有可见的**
`BaseResponse.ret` 分支；重建 codec 另外要求 `ret` 明确存在且为零，避免
把缺失值当作成功。

命令循环仅解出 `CmdId` / `CmdBuf` 并按 ID 分派：`1 ModUserInfo`、
`2 ModContact`、`4 DelContact`、`5 AddMsg`、`0x17 FunctionSwitch`、
`0x23 ModUserImg`、`0x2c UserInfoExt`。未知 ID 记录后继续。对“最小可靠
流程”，应保留每个 `CmdBuf.Buffer` 原始字节，不能把未知命令清空或猜作空
protobuf。

## 首条消息之前：证据边界

已能确定，调用 NewInit 之前必须已有授权后持久的 `SessionKey`、`Uin`、
设备 ID/version/type 和 transport cookie；这些是请求实际读取的输入。
成功响应后应保存 `CurrentSynckey` 与所有 response presence，并完成命令循环。
这在样本自己的初始化路径中是必须步骤。

但是，在目前获得的 NewInit 静态片段中，尚**不能证明** `CurrentSynckey` 或
每一种 CmdItem 是独立 `NewSendMsg` 请求的 wire-level 必填项；它们显然是
后续同步状态和本地资料初始化输入。没有线上 trace 前，不应以“首条消息可能
不直接读取”作为跳过 NewInit 的理由。

## 落地 codec 与校验

新建的 `internal/initwire` 独立负责 payload：

- `BuildRequest(loginwire.BaseRequest, user, current, max)` 固定 `zh_CN`，并
  保留两份同步 key wrapper；
- `ParseResponse` 返回 typed `BaseResponse`、两个 key、continue/select/ratio
  presence 与 `CmdList`；每条命令保存 `Raw`；
- payload 2 MiB、字段数 256、嵌套深度 12、命令数 128、单 blob 64 KiB 限制；
  `iLen` 与实字节长度不一致会拒绝；
- `ValidateNativeGate` 按样本的非空 CmdList/可访问 CurrentSynckey 假设检查；
  `ValidateForUse` 额外拒绝缺失或非零 `BaseResponse.ret`；
- `testdata` 中是以离线 `google.protobuf` 动态 proto2 descriptor 序列化的
  固定 request/response fixture；测试还读取 ABI evidence，检查
  `NewInitRequest`、`NewInitResponse`、field 1 和 repeated field 7 tag 未漂移。

业务加密、CGI envelope、HTTP、cookie 更新及任何网络重试不在这个包内；由
`business`/transport 调用者负责。

## 尚未恢复的具体问题

后续落地：`loginflow.InitializePage` / `wechat-login initialize` 已接入类型5业务封包与
MMTLS carrier，只提交一次请求。它保留私有响应文件和同步状态，明确将标记命名为
`initialization_verified`，不把 `ContinueFlag` 值解释为完整同步完成。合成 carrier
测试检查 CGI139、请求前持久化、响应保存、flag=1 时不循环，以及未知结果禁止重发。
命令 payload 暂以 raw bytes 保存；尚未完成真实授权会话上的互通。

1. `ContinueFlag` 的循环终止/再次 NewInit 的完整调用者尚未导出，不能把它
   一律视为可忽略或一律强制再请求；尤其不能把 `ContinueFlag==0` 当作本函数
   已证实的 `InitializationComplete` 前置条件。
2. `MaxSynckey` 在本函数之后的持久化消费者尚未在当前证据集中确认。
3. 命令 ID 对应 payload message 的内部字段尚未全部恢复；raw bytes 必须保留。
4. 需要在真实授权成功后的**单次、受控** integration 验证确认 envelope、响应
   ret 与保存状态；此报告及测试没有发起该验证。
