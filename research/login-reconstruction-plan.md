# 扫码登录：最小依赖图和独立实现顺序

> 历史静态规划，以下“尚未恢复”反映写作时状态。2026-10-24 自写 Go 代码已完成真实初始握手、二维码请求与状态 2 轮询；授权一次返回 -106，尚无发送会话。当前状态以根目录 README 和 `evidence/login-live-attempt-20261024.json` 为准。

本文件将当前样本的有限静态调用结果与历史公开参考实现交叉比对。它不来自运行样本，也未向任何服务发出请求。参考源码没有明确许可证，只用于理解结构，不复制为实现。当前主程序全自动分析未完成，地址与调用关系优先来自本轮 Ghidra 定点导出 `evidence/login-ghidra.txt`。

## 最短路径

```text
持久设备标识
  → MMTLS 初始握手（暂未恢复，阻断项）
  → 可选 fpinitnl 设备令牌
  → getloginqrcode (hybrid-ECDH 包装)
  → 展示二维码，保存 uuid / notifyKey / cookie / MMTLS 会话
  → 重复 checkloginqrcode (hybrid-ECDH 包装)
  → notifyData 用 notifyKey AES 解密，状态=2
  → secmanualauth (新 ECDH-713 密钥对 + 设备证明)
  → 解密会话键，持久化 UIN、cookie、session / client-session / server-session keys、登录 ECDH key、MMTLS 状态及服务器地址
  → newinit/同步，随后才可发文本
```

在样本中 `Login.GetQRCODE` 为 `0xb7dae0`，`Login.CheckUuid` 为 `0xb77f40`，`Login.SecManualAuth` 为 `0xba0f80`，`Login.Newinit` 为 `0xb9f220`；这些入口位于当前样本，而不是仅历史参考。Ghidra 显示 GetQRCODE 在 `0xb7e217` 调 MMTLS POST（`0x8f3060`），CheckUuid 在 `0xb784c0` 调同一传输入口。样本的 QRCODE 入口也调用当前的 `GenIpadLoginData`（`0xb7dc36 → 0xb98c80`）和多个设备令牌/刷新路径，说明设备资料不是发送阶段可随意省略的可选 UI 数据。

## 请求阶段

### 1. MMTLS 握手与设备资料

扫码前必须有可用 MMTLS transport。历史实现从两对 ECDH-415 密钥建立握手，保留 MMTLS 会话对象供二维码请求与轮询使用。当前样本 GetQRCODE 直接进入 MMTLS POST，说明同一会话依赖仍在，但本轮尚未恢复握手包字段与密钥派生；这是第一个硬阻断项。

当前样本还存在 `Login.createDeviceInfo`（`0xb9d700`）和多种 `Gen*/Update*LoginData` 函数。独立实现应生成一次并持久化稳定的设备 ID、机型、系统、语言、时区、品牌及相关 UUID；每次扫码重随机化设备画像会破坏会话连续性并增加失败风险。

历史实现中的 iPad 路径会先请求 `/cgi-bin/micromsg-bin/fpinitnl` 取得 device token，之后把 token 放入登录设备证明。样本 GetQRCODE 对应地调用设备令牌/刷新函数（例如 `0xb88660`、`0xb88ca0`、`0xb892e0`），所以它不应被假定可以安全省略。该路径从现有代码和 CGI 名称看是面向微信端的协议请求；在有限静态分析中未发现二维码核心流程必须调用第三方 UUID 服务。

### 2. 获取二维码

历史 iPad 路径把 `GetLoginQRCodeRequest` protobuf 用 Hybrid ECDH iOS 包装（CGI 候选 502），经 MMTLS POST 发至 `/cgi-bin/micromsg-bin/getloginqrcode`。请求包含零 UIN、设备 ID、版本、设备类型、随机 16 字节 key 和设备 token。成功响应提供：二维码二进制内容、UUID、notify key、过期时间和 cookie。

最小服务只需把二维码二进制在本地转 PNG/Base64 后呈现；历史参考中生成在线二维码图片 URL 的 `api.qrserver.com` 仅是展示便利，不是协议要求，独立实现可完全避免。

将下列数据按 UUID 暂存并设置短过期：UUID、随机 key、notify key、设备 ID/画像、cookie、MMTLS 会话、device token。历史实现用 Redis；独立版本可先使用加密的本地存储，但不得把会话密钥写进日志。

### 3. 轮询、扫码确认与授权

`CheckUuid` 发送 `CheckLoginQRCodeRequest`，历史路径使用 `/cgi-bin/micromsg-bin/checkloginqrcode`、Hybrid ECDH 包装（候选 CGI 503）和同一 MMTLS 会话。响应中的 NotifyData 要用前一步 notify key 做 AES 解密，再 protobuf 解出二维码状态。只有 status=2 才进入授权；样本 CheckUuid 在成功解包后于 `0xb787f1` 调后续登录流程 `AwakenLoginNew` (`0xb76120`)，所以当前版本仍存在“轮询成功再转后续授权”的结构。

从通知中得到的用户名、密码/临时凭据及新 cookie 是 secmanualauth 的输入，不能把 UUID 本身当成登录态。

### 4. SecManualAuth 与会话材料

历史参考通过 `/cgi-bin/micromsg-bin/secmanualauth`（候选 CGI 252）提交 RSA 部分、AES 设备部分和新生成的 ECDH-713 公钥。成功响应含服务端 ECDH 公钥、加密 session key、auto-auth key、server/client session keys、UIN、账户资料及服务器地址。客户端以私钥和服务器 ECDH 公钥派生登录 ECDH key；其 MD5 用于解开 session key。

为后续发送至少须持久化：UIN、cookie、sessionKey、clientSessionKey、serverSessionKey、登录 ECDH key、MMTLS session material、设备资料、客户端版本/设备类型、短连接主机。`newinit`/同步的确切输入和是否必须在首发文字前完成尚未在当前样本完成验证；实现时应把它列为登录后健康检查，而非假设已可跳过。

## 外部依赖判断

| 依赖 | 当前证据 | 最小实现结论 |
|---|---|---|
| 微信协议端 MMTLS / CGI | 样本 QR 与轮询直接调 MMTLS POST | 必须恢复或使用经过授权的互通 transport |
| 设备 token `fpinitnl` | 历史路径明确使用；样本 QR 调设备 token 函数 | 视为必须，除非受控测试证明当前服务端接受无 token |
| 第三方二维码图片服务 | 仅历史 UI 便利代码 | 不需要；本地渲染二维码 |
| 第三方 UUID 服务 | QR 核心代码/样本调用图中未见必需证据 | 当前没有证据表明必须使用 |
| 滑块/OCR/短信服务 | 样本另有 LoginOCR、WxSliderOCR、WeChatSMS 函数 | 非正常 QR 成功路径的风控分支；扫码主流程不先依赖，但可能成为实际登录阻断 |
| libv08/RQT | 用于业务请求 Pack；已知 dynlib→lib/libv08.so→`rqtx` 间接绑定 | 二维码 Hybrid ECDH 路径本轮未证明调用它；文本发送前仍需要正确 RQT |

## 实现顺序和阻断项

1. 先定义加密会话状态和持久化格式，禁止日志记录 key、cookie、凭据或二维码 token。
2. 先将 MMTLS 初始握手、已握手 POST、Hybrid ECDH 包装/解包建立为可用的离线向量测试接口。这是 QR 之前不可绕过的阻断项。
3. 从当前 mm.proto 描述符生成 QR、轮询、授权相关 protobuf；本轮没有重复提取描述符。
4. 完成设备画像与 `fpinitnl`，再实现 `getloginqrcode`、本地二维码渲染和有上限的轮询。
5. 完成 notify AES 解包、状态机和 `secmanualauth` 的 ECDH-713 派生与响应状态保存。
6. 用 newinit/同步验证得到会话可用后，再接文字发送封包。只有当 RQT、Pack、MMTLS 和会话材料均匹配时才会互通。

不能仅凭 protobuf 或二维码 URL 复现登录。尚未恢复的 MMTLS 握手、Hybrid ECDH 细节、设备 token 证明和风控分支决定了是否可从零会话登录。

## 证据

- `evidence/login-ghidra.txt`：当前样本的定点伪代码与调用边。
- `evidence/login-functions.tsv`：导出入口和地址。
- `evidence/login-ghidra.log`：Ghidra 处理日志。
- 已有 `evidence/main-ghidra-selected.txt`、`main-ghidra-followup.txt` 和 `main-exact-literals.json`：dynlib→`lib/libv08.so`→`rqtx` 的已恢复绑定；本文件复用该结论。
