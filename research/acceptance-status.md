# 第一阶段完成条件核对

**后续机器人阶段也已验证：2026-10-24 文件传输助手 `/ai` → DeepSeek → 自动回复，以及“机器人测试群”真实 @ 触发回复，均有接受回执和用户确认；普通群消息不触发。** 见 [机器人验收](evidence/bot-deepseek-acceptance.json)、[运行与 Docker 说明](../docs/guide/USER-GUIDE.md)。已增加 WSL 用户服务托管、重启续接进度及不自动重试。完整容器内真实授权/回复和云服务器长期运行仍未验收。

**当前结论：2026-10-24 第一阶段“真实后台发送文字”已完成。** 后续采用从固定公开源码构建的 Linux 原生协议客户端，真实登录测试小号，单次向 `filehelper` 发送“后台发送测试”。服务端消息 ID `6809579812965061880`、客户端消息 ID `788294973`，用户随后明确确认手机收到。见 [当前脱敏验收](evidence/linux-native-acceptance.json)、[构建记录](evidence/linux-native-build.json) 和根目录 [运行说明](../README.md)。

| 第一阶段要求 | 当前判定 |
| --- | --- |
| 从源码构建，运行不依赖原闭源 main/lib | 已通过：固定公开 Rust 源码与锁文件，配合自写 Go 适配器；不宣称上游代码全部原创或统一宽松许可 |
| Windows 本机验证 | 已通过：WSL2 Ubuntu 后台，无微信桌面进程；Go API 同时可编译为 Windows 程序 |
| Linux / Docker 交付 | Linux 原生真实发送通过；完整镜像构建和容器 API 检查通过。容器内新授权、云主机长期发送未实测 |
| 可调用 API 与配置/错误处理 | 原生 API 完成真实发送；Go API 对已登录账号 readiness=200，适配器覆盖 queued/submitting/accepted、错误回执和禁止重试；未额外发送第二条消息 |
| 一条真实消息服务端接受且手机可见 | 已通过：用户明确确认，提交一次，没有自动重发 |
| 后续机器人功能 | 群消息收发、DeepSeek、开机自启与长期稳定性不在本阶段完成范围内 |

下面是 Linux 路线成功前的 iPad 历史恢复记录，**不代表当前项目总体状态**。iPad 实验后端仍未授权成功，未用于本次真实发送。独立历史副本见 [acceptance-ipad-history.md](acceptance-ipad-history.md)。

---

核对日期：2026-10-24。目标仍是以自写协议完成真实后台文字发送，不能以离线测试缩减目标。

## 当前恢复推进

用户已澄清设备协议不限于 iPad、安装包来源不限于官方，后续又要求继续。此前的 iPad 官方包
获取阻塞不再代表所有可行路线均已耗尽。当前正在从固定公开源码
`thisnick/agent-wechat@7095ef01e0cd3d5b567bcb6970f379dcd88bbfa8` 构建独立 Linux
原生协议客户端；它不运行官方桌面程序或原闭源 main/lib。fresh-profile 登录实现存在，但
仍需以本测试账号实际授权来验证，不能引用仓库测试直接宣布成功。

同时已从用户允许的第三方注入包中，通过公开 HTTP Range + ZIP CRC 检查提取
`8.0.78.34` 的 Info.plist 和 ARM64 主程序，样本 SHA-256 为
`5b6d50c920d6d4933932d114368c4049af817104f67459db76383f68b59efde8`。
只做静态分析，不安装/执行插件；见 `evidence/public-ipa-8078/inventory.json`。
用户已指定第一条测试消息发给测试小号自己的 `filehelper`，文本为“后台发送测试”。
新路线尚未生成可用账号会话，也没有发送真实消息。后文阻塞复核保留为历史记录。

| 原始要求 | 当前权威证据 | 判定 |
| --- | --- | --- |
| 从源码构建，不重新包装闭源 main/libv08.so | Go 标准库应用、Dockerfile 构建源码；RQTX VM 和 SAE 表解释器有源码 | 代码路径已实现；仍依赖不透明 VM 字节码与 SAE 表数据，不能称完全摆脱样本数据 |
| Windows 本机后台运行 | dist/windows-amd64 三个程序；send-smoke.json 中 health=200、无会话拒绝发送 | 已验证进程/API启动；不是微信发送验收 |
| Ubuntu/Linux 与 Docker | dist/linux-amd64 三个程序；本地 Docker Linux 镜像构建及后台检查 | 已构建并在 Linux 容器验证基础服务；未验证云服务器真实发送 |
| 可调用发送接口、配置和错误处理 | httpapi/native/send CLI 源码与测试，Token、白名单、未知结果不重试、README | 已实现并离线/本地验证；远端互通尚未证明 |
| 从未登录小号取得二维码并确认 | login-live-attempt-20261024.json、login-qr4-attempt.json、upgrade-authorization-20261024.json：真实 poll=2 | 已验证；最新 login-5 手机完成全部确认并已提交一次授权 |
| 自写设备令牌流程 | fingerprint-live-20261024.json：真实 ret=0、令牌和软数据存在 | 已真实验证；不等于账号授权成功 |
| 完整账号授权 | upgrade-authorization-20261024.json：login-5 使用较高候选版本、完整候选附加信息及真实令牌，返回 -106，正文提示应用版本过低；没有会话文件 | 未完成 |
| 登录后初始化 | initwire / InitializePage / initialize CLI 和独立 protobuf/合成 carrier 测试 | 已实现候选链路；缺真实授权会话，未互通验证 |
| 真实后台发送一条文字并校验回执 | 没有真实发送回执，也没有可用账号会话 | 未完成，不标记目标成功 |

## 当前具体阻塞

login-4 已收到用户完成全部手机确认的反馈，程序实际取回状态 2，并提交一次完整候选授权。
微信返回 `-106`，解码后的 Content 为“你的应用版本过低，请升级至最新版本后再登录”。
因此当前阻塞已从“等待扫码”变成“登录 profile/协议兼容性未通过”。该提示是直接响应证据，
不证明版本号是唯一问题，也不能据此认定账号被封；设备证明的独立编码测试仍不能替代原实现互通对照。
临时凭据已标记提交过，未重复授权，未生成发送会话，未发送任何真实消息。

已校验并只读提取上游 2026-10-23 发布的 v3.2.3/latest 镜像，manifest digest 为
`sha256:f48d262640886ffa8d5104190f0d20a6219ae33a0fdbea2a80d8875d85997694`。
逐个 ELF 映射及 RIP 相对加载指令确认：iPad 默认版本仍为 `402668320`（`0x18003b20`），
设备类型及 bootstrap 公钥也相同。存在调用方传入版本的路径，但没有已验证的新值。
见 [升级审计](version-upgrade-evidence.md)、[静态比较](evidence/version-update/profile-comparison.json)。
新发布镜像本身不能作为登录已经修复的证据。

## 显式版本升级对照

用户要求继续升级后，从另一公开发布物 `WeChatPadPro v2.01` 的符号和调用方读取
`ClientVersion=402669099` / `0x18003e2b`，详见
[候选静态证据](evidence/padpro-upgrade/version-profile.json)。发布标签本身不用于推导版本数值。
本地已增加有来源的候选配置、保留设备身份的迁移命令及全链路版本传递。
为区分“版本字段未更新”与其它兼容性问题，进行一次只替换版本的鉴别性实验，保留原有
设备类型、bootstrap 和 v8 证明；没有替换候选包中的 v3 SAE 表，也不声称完成候选整套协议移植。

真实结果：fpinitnl 返回 0、新令牌和软数据存在；login-5 的扫码状态为 2；随后仅一次账号授权
仍返回 -106，响应 Content 仍为“应用版本过低”。见
[预检](evidence/upgrade-preflight-20261024.json)、[授权结果](evidence/upgrade-authorization-20261024.json)。
没有账号会话、没有真实发送。此次更高版本实验未解除阻塞；后续需要更近期的可核验版本，
或具体证明/设备/封包兼容性差异，不能据此宣布“已修复”。

## 官方源后续核对

腾讯官网和 Apple 官方 API 都确认当前 iOS/iPad 商店版 `8.0.80`，发布日期 2026-10-23；
腾讯 iOS/iPad 下载入口均指向 App Store。已保存官方页面和 SSR JSON 下载配置，未在检查的
公开载荷中发现内部 iPad `ClientVersion` 或直接 IPA 地址。详见
[官方源报告](evidence/official-upgrade/report.md)。

用户要求自行获取安装包后，已安装项目局部 IPATool 2.7.0 并校验发布哈希，实际请求 App Store
`414478124` 的 `ipad` 包。独立工具资料中没有 Apple 账号，下载明确以 `failed to get account`
失败；没有生成 IPA。该结果见 [下载尝试](evidence/official-upgrade/download-attempt.json)。
仍不能将官方商店显示版本直接换算为内部协议值，不能将安装下载工具当作协议升级成功。

## 阻塞复核

2026-10-24 再次直接核对：`secrets/wechat.session.json` 和 `secrets/wechat-5.session.json`
均不存在；官方 IPA 输出数量为零；wechat-login / ipatool 均无运行进程；两个版本调查与
版本传递子任务均已结束。所有六个构建产物的哈希仍与构建记录一致，但不构成微信发送证明。

同一“缺少能通过账号授权的新版 iPad 协议材料”条件已持续出现在官方包获取及随后的
两轮目标复核中。已检查的公开源、镜像和候选均未解除条件，官方包实际下载又缺少 Apple
账号认证。当前没有已建立依据、可独立继续的升级或真实发送步骤，目标应标记 blocked，
保留原始范围，不标记完成。恢复需要取得可分析的官方新版 iPad 包或可核验的新版协议配置。

## 恢复后的最短验收路径

1. 先取得可核对的新版 iPad profile/协议实现，或找到并验证当前设备证明/编码与原实现之间的具体差异；保持账号设备身份一致。
2. 修正有证据的差异并完成本地核对后，用户在线时生成新二维码，立即有界轮询，手机确认后提交一次授权。
3. 只有收到并校验真实会话，才验证 newinit，保留命令和同步键，区分单次响应和全量同步完成。
4. 对用户确定的测试目标发送一条文字，检查 client ID、收件人及非零服务器消息 ID；结果未知不自动重发。
5. 记录真实结果后才能标记目标完成；当前目标未达成。

所有链接中的脱敏记录均不包含账号密码、设备令牌、会话密钥或原始授权响应；敏感恢复材料只留在私有 secrets 目录。
