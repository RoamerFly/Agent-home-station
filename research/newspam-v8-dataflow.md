# `GetNewSpamDataV8` 的静态数据流审计

> 历史中间审计。后续逐字段恢复已纠正 context、token 最终来源和 NewClientCheckData 中间层，并实现完整 builder。以 `spam-v8-reconstruction.md` 和当前源码为准；下文未解项和早期推断不代表当前状态。

日期：2026-10-24。范围是固定样本 `research/samples/main.elf.bin`、已有的
`research/evidence/device-v8-ghidra.txt` 和 `main-functions.json`。所有结论均来自
反编译文本、ABI 类型元数据和静态反汇编；没有启动 Ghidra、执行样本、读取会话/密钥或发起网络请求。

本文件记录“此样本怎样构造字节”的可复核证据，并不表示任一外部服务会接受重新实现的结果。

## 返回对象和包裹层

`wechatdll/clientsdk/extinfo.GetNewSpamDataV8` 入口为 `0x00b40ee0`。它分配并填充
`wechat.SpamDataBody`（Go ABI type `0xf582e0`，792 bytes），在 `0x00b45238` 调用
`github.com/gogo/protobuf/proto.Marshal`，随后按已知 v8 层调用：

```text
SpamDataBody --gogo proto.Marshal--> bytes
  --v08.Compress (0x007bdd00)--> bytes
  --ccdata.EncodeZipData (0x007c9bc0)--> bytes
  --> wechat.DeviceRunningInfoNew{Version="00000008", Type=1,
                                  EncryptData=<above>, Timestamp=<current>,
                                  Unknown5=5, Unknown6=<zero pointer>}
  --gogo proto.Marshal (0x00b453fa)--> function return
```

因此 `CryptoData.EncryptData` 收到的是外层 `DeviceRunningInfoNew` 的 protobuf，不是
裸 `SpamDataBody`。`DeviceRunningInfoNew` 的类型、tag 和固定值已由 ABI metadata
核对；`Unknown6` 的零值 pointer 是否编码进 wire 仍需使用同版本 gogo marshaler 做离线
对照。

## 已解析的 protobuf 类型

| ABI 地址 | 类型 | 此函数中的角色 |
|---|---|---|
| `0xf582e0` | `wechat.SpamDataBody` | 主设备/运行环境主体，最终先被 gogo marshal |
| `0xecb280` | `wechat.FileStat` | 文件时间统计的嵌套消息 |
| `0xef74a0` | `wechat.Statfs` | 文件系统统计的嵌套消息 |
| `0xecb180` | `wechat.NewClientCheckData` | 函数引用/分配；在当前导出截点不能完整可靠映射到返回字段 |
| `0xef7360` | `wechat.DeviceRunningInfoNew` | 上述最终 v8 外层 |

这些名称、字段名和 tag 来自固定 ELF 的 Go 1.24 `abi.StructType`。辅助静态解析器为
`research/scripts/recover_newspam_v8_structs.py`；它只读取 ELF。

## 已能一一对应的 `state` 输入到 `SpamDataBody` 字段

下表只收录“源地址链、目标 struct offset 和 protobuf tag 三者都能在反编译/汇编中连起来”的项目。
`state` 指调用参数；中间 `+0x3e8` 是样本中反复出现的嵌套配置/设备对象，尚未恢复其
Go 类型名，不能把偏移当作公开 schema。

| `SpamDataBody` 字段（struct offset / tag） | `state` 读取链 | 转换 | 静态证据 |
|---|---|---|---|
| `DeviceToken` (`+0x1d8`, tag 64) | `state+0x278 -> +0x30 -> +0x28 -> +0x30/+0x38` | 将得到的 string bytes 复制到新的 string 值 | Ghidra `0xb435..0xb436`，随后写 `SpamDataBody+0x1d8` |
| `SystemVersionCtime` (`+0x1f8`, tag 82) | `state+0x3e8 -> +0x168 -> +0x28` | 读取值乘 `1_000_000_000`，`EncodeUInt64(value, 0xd91ca739, ctx)` | `0xb43912..0xb4395e` |
| `DyldSharedCacheArm64Ctime` (`+0x200`, tag 83) | `state+0x3e8 -> +0x170 -> +0x28` | 乘 `1_000_000_000`，`EncodeUInt64(value, 0xa3071842, ctx)` | `0xb439a9..0xb439f5` |
| `PrivateVarDirCTime` (`+0x208`, tag 84) | `state+0x3e8 -> +0x178 -> +0x28` | 乘 `1_000_000_000`，`EncodeUInt64(value, 0x5a5f1852, ctx)` | `0xb43a45..0xb43a91` |
| `EmptyString` (`+0x210`, tag 85) | 无 | 分配 string 零值 | `0xb43aba..0xb43ae?` |
| `IsCaptured` (`+0x218`, tag 86) | 无 | 分配 `uint64` 零值 | `0xb43afb..0xb43b2?` |

函数开始还读取 `state+0x68/+0x70` 并调用 `baseutils.IOSUuid` (`0x007c0a00`)；输出被转换为
字节序列后参与主体构造。当前证据不足以把这两个源字段安全地标定为 `SpamDataBody` 的某个
具体 tag，故没有用“设备 ID”之类名称猜测它们。

## 时间字段与已确定的本地计算

函数两次读取当前时间（调用 `0x004eecc0`）。静态数据流明确生成并写入主体：

| 字段 | tag | 计算 |
|---|---:|---|
| `TimeStamp` | 2 | 当前时间的秒样式 `uint32` 值 |
| `Now` | 70 | 基于同一时间源、掺入函数内已取得值的位运算；精确算式见 `0xb43726..0xb43819` |
| `NowUsec` | 71 | 时间源除以 `1000` 的 `uint32` 样式值 |
| `NowUsecScale` | 72 | `NowUsec * 0x68323 + 0x11` |

这三项分别写 `SpamDataBody+0x1e0/+0x1e8/+0x1f0`。这里的命名由 protobuf tag
metadata 给出；“秒/微秒”是字段名，不应用名称替代对时间 API epoch 和整数截断的离线验证。

## v08 编码调用顺序和常量

`EncodeString` 为 `0x007bd760`，`EncodeUInt64` 为 `0x007bd9a0`，`Rqtx` 为
`0x007bd5a0`。所有这些调用的第三/最后一个实参都复用函数早期构建的 context（在
反汇编中落在 `rsp+0x88`）；其产生算法尚未恢复，因此不能把它替为零值。

以下是后半段可由调用点寄存器直接核对的顺序（地址即 call-site）：

| call-site | 原语 | value 来源 | 常量/ctx |
|---|---|---|---|
| `0xb43936` | `EncodeUInt64` | `state+0x3e8 -> +0x168 -> +0x28`, 再乘 `1e9` | `0xd91ca739`, ctx |
| `0xb439cd` | `EncodeUInt64` | `state+0x3e8 -> +0x170 -> +0x28`, 再乘 `1e9` | `0xa3071842`, ctx |
| `0xb43a69` | `EncodeUInt64` | `state+0x3e8 -> +0x178 -> +0x28`, 再乘 `1e9` | `0x5a5f1852`, ctx |
| `0xb43b6a` | `EncodeUInt64` | `state+0x3e8 -> +0x190 -> +0x0` | `0x99687b8a`, ctx；最终主体字段尚未静态回填确认 |
| `0xb43bb9` | `EncodeString` | `state+0x3e8 -> +0x190 -> +0x8/+0x10` | `0xe2868e99`, ctx；最终字段未确认 |
| `0xb43c20` | `EncodeUInt64` | `state+0x3e8 -> +0x190 -> +0x18` | `0xc36fa9a2`, ctx；最终字段未确认 |
| `0xb43c6f` | `EncodeString` | `state+0x3e8 -> +0x190 -> +0x20/+0x28` | `0xb62136b0`, ctx；最终字段未确认 |
| `0xb43cda` | `EncodeString` | `state+0x3e8 -> +0x190 -> +0x30/+0x38` | `0x47ee23c4`, ctx；最终字段未确认 |
| `0xb43d41` | `EncodeUInt64` | `state+0x3e8 -> +0x190 -> +0x40` | `0xa19a7ed5`, ctx；最终字段未确认 |
| `0xb44080` | `EncodeUInt64` | 固定值 `1` | `0x75a9f3fa`, ctx |

在 `0xb440e0..0xb4458e` 和 `0xb44ca0..0xb450ae` 还有连续的 `EncodeString`/
`EncodeUInt64` 调用。它们把编码结果组装进 `Statfs`、`FileStat` 和 `SpamDataBody`
的嵌套/repeated 区域。由于 Go write-barrier 路径把临时 stack slots 分支化，当前静态
导出无法在不重新做 SSA 数据流的情况下可靠给每个调用贴上最终 tag；本报告不以位置猜测 tag。

## `GetFileInfo` 和未解的数据

`0xb422c8` 直接调用 `wechatdll/clientsdk/extinfo.GetFileInfo` (`0x00b45500`)；结果随后
经 `Rqtx`（至少 `0xb42439`、`0xb42538`）及 CRC32 处理，并流入主体的文件信息相关区域。
`SpamDataBody.AppFileInfo` 是 tag 46 的 repeated `wechat.FileInfo`，且还存在
`AppFileInfoCrc`（tag 60），但 `FileInfo` 的 gogo type/layout 和 `GetFileInfo` 的参数
语义不在当前已恢复 metadata 范围内。它是完成最小设备数据路径前必须单独恢复的阻塞项，不能
用空列表、空字符串或伪造时间戳替代。

同样未解的项目：

1. v08 context（所有 `Encode*` 的 ctx）及 `Rqtx` 的字节级语义；
2. `0xb440e0..0xb450ae` 的临时值到全部 `SpamDataBody` field 的精确 SSA 映射；
3. `state+0x3e8`、`+0x278` 所属对象的类型、生产、持久化和刷新规则；
4. gogo protobuf 对所见零值 pointer 的具体 wire presence；
5. `Compress` / `EncodeZipData` 的准确格式。

这些未解项意味着目前只能把上表作为离线恢复任务的字段映射，不能据此构造或提交“近似”设备数据。
