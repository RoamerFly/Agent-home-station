# agent-wechat 原生 Linux 协议审计

审计日期：2026-10-24（只读）  
审计对象：`C:\Temp\wechat-bot-research-20261023\agent-wechat`，提交 `7095ef01e0cd3d5b567bcb6970f379dcd88bbfa8`（`2026-10-23T19:16:34-07:00`）。  
边界：未构建、未运行、未发起登录、未读取任何账号或官方 profile；结论仅来自该公开源码树的静态审阅。

## 结论

该项目现在确实包含不依赖官方 Linux 客户端运行时的 Rust 原生协议客户端，但不能把“源码有 fresh login 路径”表述成“任意微信号已验证可从零稳定登录”。可复核的 fresh 路径是：

```text
wx accounts create --alias test
wx --account test auth login
  -> 生成/保存 Linux DeviceProfile
  -> CGI 502 getloginqrcode
  -> CGI 503 checkloginqrcode
  -> 获批准后 P-224 manual-auth，CGI 252 secmanualauth
  -> 收到可接受登录态后，managed-profile bootstrap 写入七个加密数据库和 saved-login 文件
```

源码和项目自身说明都把这条路径定位为**实验性**：其受控验证及本地测试说明接受登录材料后可以创建/重开 profile，但官方客户端“完整首次初始化、搜索索引和 UI 渲染”仍未建立。此次审计没有做真实服务器交换，故服务器是否会接受某个新测试号仍为未知。

`wx auth login --official` 不是上述空 profile 初始化命令。它选择 daemon host 上一个**已初始化且已停止的 Linux 官方 profile**，发现/绑定其账号身份、key-info 与可用 saved-device trust；若 trust 不可用才可能走 QR。CLI 明确把 macOS/Windows profile 标为未经验证。因此不能用一个空目录或仅“新建的 profile 文件”替代已登录官方 profile。

## Fresh login 的证据与强度

| 结论 | 静态证据 | 强度 / 限制 |
| --- | --- | --- |
| 原生 fresh 操作有公开命令入口 | `packages/native-client-rust/README.md:121-161` 直接给出 `accounts create` 再 `auth login` | 文档声明；不是一次本审计执行 |
| 运行时在无既存状态时生成 Linux device 并发起 QR | `wechat-core/src/runtime.rs:779-823`：无 `device` 记录时 `DeviceProfile::generate`，随后 `start_profile_qr_login` | 源码调用链 |
| QR request、轮询、批准后授权均存在 | `wechat-protocol/src/client.rs:570-612`（CGI 502），`715-755`（CGI 503），`804-842`（P-224 + CGI 252） | 源码实现，不代表线上成功 |
| 接受登录态后才创建新官方格式目录 | `wechat-core/src/bootstrap.rs:75-126`：要求 `managed`、已接受 state、匹配 accepted generation、32-byte database password、format key | 源码状态门控 |
| 本地测试验证 seven DB + reopen | `wechat-core/src/runtime.rs:2801-2836` | 合成 accepted state；不能证明服务端接受 fresh QR |
| 项目自己承认官方首次启动未验证 | `FEATURES.md:85-91`；`README.md:161` | 限制优先于功能宣传 |

## `--official` 的准确语义

根 README 要求关闭官方 Linux app，`--official` 在 daemon host 上选择现有文件，并“准备 identity/key setup”；可指定绝对 profile 路径（`README.md:30-40`）。native README 进一步说明该命令发现或选择 operating files、注册账号、绑定 identity、尝试 saved-device，并在需要批准时给 QR（`packages/native-client-rust/README.md:97-119`）。CLI 参数注释则限定为“supported stopped Linux files”，并明确 macOS/Windows 未验证（`crates/wx/src/main.rs:437-444`）。

因此：

- 可将它看作对**既有、停止运行**的官方 Linux profile 的接管入口；
- 不可将它看作一个无需身份/密钥材料的 profile 初始化器；
- 即使 profile 可被选中，项目也明确不保证任意未知官方布局都能接管成功。

## Linux 协议 profile 与当前 Go/iPad 实现的比较

| 项目 | agent-wechat Linux 原生实现 | 对当前 Go iPad 代码的含义 |
| --- | --- | --- |
| device tuple | `DeviceProfile { device_id, device_type, client_version }`；`generate` 固定 `-227271398`，即无符号 `0xf2741d1a` | 这是 Linux family，不能替换 iPad 的版本或 device type |
| 默认设备类型 | `UnifiedPCLinux x86_64`（runtime `745, 784`） | 不能与 iPad Air / iPadOS profile 混用 |
| 校验 | `DeviceProfile::validate` 强制上述 exact client version（`client.rs:51-75`） | 它不是“自由选择新版本”的实现证据 |
| base request | field 3 device id，field 4 signed client version，field 5 device type（`wire.rs:24-31`） | 证明三项应作为一致 tuple 被贯穿；可作为我们 `WireProfile` 数据流设计的独立结构性交叉检查 |
| prelogin 信任材料 | `business.rs:12-30` 从带版本的 Linux region pin fixture 选 P-256 ECDH point / ECDSA PEM（region 1→10014，2→50005） | 说明 pin 应有来源、版本与选择规则；这些 pin 不属于 iPad bootstrap，禁止直接复制 |
| 登录完成 ECDH | P-224 (`SECP224R1`) 用于 manual/auto auth（`client.rs:804-842`、`wire.rs:203`） | 仅说明同类系统可能分层使用不同曲线；不能替代 iPad 认证或 device-proof 实现 |

该项目的 native `DeviceProfile::generate` 以随机 16 bytes 构造以 `l` 开头的 ID、并固定 Linux version（`client.rs:51-75`）；运行时 fresh 分支也确实采用它（`runtime.rs:779-797`）。这可作为“新 profile 的设备身份应先生成并持久化，而非每次请求随机变化”的实现模式证据，但不是 iPad device ID 的算法或可移植值。

## 可复用与不可复用的边界

可以恢复/借鉴的，是经独立实现佐证的**结构**：QR 获取→轮询→批准→最终授权的状态机，device/version/type tuple 的请求字段、持久化和一致使用，以及需要有版本化来源的预登录公钥 pin 选择。

不能直接搬运的，是任何 Linux 数值、pin、曲线、数据库格式或 auth 加密表。该源码的 prelogin outer packet 还使用其 own envelope policy，Linux profile 对应固定 version 与 Linux 官方 profile family；它没有为当前 Go 的 iPad `sae` / device-proof 链条提供可验证的新版替换材料。尤其不能把候选包中 `sae version 00000003` 接到当前 `00000008` 加密表。

## 对当前升级工作的行动建议

1. 保持现有 Go 的 `WireProfile` 仅做 tuple 传递与一致性校验；候选 iPad 值必须来自与 iPad 登录路径一致的、可复现的静态/动态证据。
2. 将 agent-wechat 视为可评估的**独立 Linux 基座候选**，而不是 iPad Go 协议的版本资料来源；若验证其 fresh login，必须在隔离测试账号、Linux 环境和明确账号授权下单独执行。
3. 不要用 `--official` 对真实主 profile 做试验。它要求官方 app 停止且其 own 文档仍保留 profile 兼容性限制。

机器可读证据及源文件 SHA-256 见 [agent-wechat-native-audit.json](evidence/agent-wechat-native-audit.json)。
