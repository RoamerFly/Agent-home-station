# 微信闭源协议镜像：离线安全审计与逆向可行性报告

日期：2026-10-23（Asia/Shanghai）。已完成目标模式约定的固定样本静态审计；没有启动目标程序、登录微信、联系样本内第三方服务或测试真实收发。本文取代同目录的初步报告，判断限于下列 digest。

## 结论

**可以继续逆向，已经取得部分反编译结果；目前不足以把该镜像当作可信生产依赖，也没有证据将它定性为后门。** 已发现的风险包括第三方 HTTP 请求、登录相关数据与日志、闭源更新、部署权限及版本配套。

对于 Windows 试验后迁移 Ubuntu/Docker 的微信群 DeepSeek 机器人，建议自行掌握消息处理、群白名单、模型调用和存储，把闭源镜像限定为可替换的协议适配层。它适合隔离验证；重要账号、重要群及已有业务主机不应作为首次试验对象。DeepSeek 密钥只放在自己的业务服务内。登录可用性、在线稳定性和账号风险尚未验证。

## 样本身份及验证

| 项目 | 固定内容 |
| --- | --- |
| 镜像 | `registry.cn-shenzhen.aliyuncs.com/houhou/wechat-ipad` |
| manifest digest | `sha256:2d7a2c9084fc8a6c354a4961312736abe36d1302a9910eb7e855899580f1adfc` |
| 平台及大小 | Linux amd64；14 层，压缩层合计约 43.13 MiB |
| config 创建时间 | `2026-09-13T15:10:02.507068507Z` |
| 工作目录及 CMD | `/usr/wic-go`；`./entrypoint.sh` |
| 用户 | image config 未指定 User；未覆盖时按 Docker 默认用户运行 |
| main | 24,360,624 字节；SHA-256 `9b0f99ad74f11426aeb50b1c543cfadf3562338336d15dd4bc20e306089b0b2f` |
| lib/libv08.so | 242,120 字节；SHA-256 `c89374360a439abb24bf551eda04c3001fda4b58a2bf33d9e610222382796ddf` |

已验证原始 manifest 字节、config、全部 14 层 SHA-256，并按层内最后写入记录核对 main、libv08.so、entrypoint.sh、swagger.json 与提取文件一致。见 [验证结果](evidence/layer-verification.json)。哈希绑定内容，不证明发布者身份或内容安全；结论不自动适用于未来的 latest。

## 主要发现

### F1：滑块服务存在明文 HTTP 提交路径

[entrypoint.sh](evidence/entrypoint.sh) 第 47–48 行直接写入：

```text
ocrurlgo  = http://47.119.158.126:5550/unban?tick=
ocrurlhgo = http://47.119.158.126:5550/unbanlife?huaKuaiUrl=
```

`WxSliderOCRRequestS`（main `0xba9f40`）和 `WxSliderOCRRequest`（`0xbacb00`）读取对应配置，拼接传入字符串、构造 GET 请求并调用 HTTP 客户端。这确认了提交相关参数的代码路径，不只是 URL 字符串存在。默认 HTTP 会把请求 URL 和参数暴露给服务及可见明文链路的参与方；参数完整来源、敏感性和实际触发情况仍需隔离运行观察。

没有访问该 IP，未确认运营者或当前服务状态。入口脚本没有提供替换这两个地址的环境变量，不能假设设置一个变量即可关闭依赖。证据：[主程序伪代码](evidence/main-ghidra-selected.txt)，按上述函数名定位。

### F2：UUID 流程依赖远端返回值，并打印完整 URL

`getEncodedUUID`（`0xba6780`）读取 `uuidurl`，追加 **`/api/v1/uuid`**，构造含 `data`、`device_type`、`client_version` 的 GET 请求。`data` 为调用者传入的字符串；完整 URL 被传入标准输出打印路径，随后发出请求并解析响应。此函数内没有看到显式设置客户端总超时。

原始 ELF 和 objdump 确认 `runAllSteps` 在 `0xba8c93` 直接调用该函数，之后继续把返回值用于 `precheck` 等步骤。因此已确认静态调用关系及远端结果依赖。**未将 data 认定为微信密码、完整会话或聊天内容**：完整上游来源、触发条件及后续步骤的所有网络行为尚未恢复。

UUID_URL 若使用 HTTP，也存在明文链路风险；即使用 HTTPS，完整 URL 日志仍应按敏感信息处理。证据：[精确字符串](evidence/main-exact-literals.json)、[直接调用点](evidence/uuid-direct-callers.json)、[UUID 流程](evidence/main-ghidra-uuid.txt)、[请求函数](evidence/main-ghidra-selected.txt)。

### F3：接口有实际签名校验，不能由 Swagger 推断裸奔

Swagger 声明 166 个路径，缺少顶层 security/securityDefinitions；二进制中却存在实际保护：

- `InitAuthRuntime`（`0x8fa520`）要求 `ROBOT_CODE`、`ROBOT_AUTH_KEY_ID`、`WECHAT_CLIENT_HOST` 和两把方向密钥；此样本要求 key ID 为 `v1`。
- 两把密钥变量为 `ROBOT_AUTH_CLIENT_IPAD_KEY`、`ROBOT_AUTH_IPAD_CLIENT_KEY`。`DecodeKey` 解码 Base64，要求至少 32 字节。
- `VerifyRequest`（`0x8f7ba0`）包含 HMAC-SHA256、规范化请求、时间戳、nonce 重放缓存及 32 字节签名比较。哈希构造指针已映射到 `crypto/sha256.New`（`0x5a0480`）。未完成所有边界条件、并发及拒绝服务审计。
- `routers.init.1` 为 `/*` 注册 middleware；文件内指针解引用确认其指向 `middleware.init.func1`（`0xc6e720`）。它跳过 OPTIONS，其余请求调用 `VerifyClientRequest`，错误路径设置 401 并返回错误响应。
- pprof 包装函数（`0xc99d80`）也调用 `VerifyClientRequest`；不能由此保证其他所有诊断路径均受保护。
- `main.main` 首先初始化鉴权，错误分支 panic，成功后才进入常规服务初始化。

读取两把方向密钥后存在 Unsetenv 调用，但这不保证清除全部内存副本，也不自动清除 Docker 保存的容器环境配置。签名提供的完整性保护不能隐藏消息；入口脚本的业务回调仍配置为 `http://...`。

证据：[主程序伪代码](evidence/main-ghidra-selected.txt)、[鉴权补充](evidence/main-ghidra-followup.txt)、[精确指针](evidence/main-exact-literals.json)。

### F4：公开容器创建代码与样本启动要求存在配套缺口

`hp0912/wechat-robot-admin-backend` 提交 `edcc5c42d0b089832593af2079a03bc6b0800dc2` 的 `service/robot_manage.go:254–265`，只显式传入 WECHAT_PORT、Redis 参数、WECHAT_CLIENT_HOST 和 UUID_URL，缺少上述 ROBOT_CODE 和鉴权密钥变量。

在没有额外注入或配套变更时，不能期待这段创建代码直接启动本样本。公开源码与发布镜像的构建对应关系未知，因此这是配套缺口，**不是已执行过的启动失败测试**。应先核对版本及密钥分发，不能靠删除鉴权解决兼容性。

该路径没有显式 PortBindings/PublishAllPorts，但使用共享 `vars.DockerNetwork`：既不等于公网暴露，也不是每个机器人独立隔离。该路径没有传入 OPENAI_API_KEY，不能把管理后端持有模型密钥说成协议容器持有。证据：[容器创建分析](dynamic-container-audit.md)、[冻结源码](evidence/admin-robot_manage.go)。

### F5：配置、Redis 和诊断输出应按敏感数据保护

入口脚本把 Redis 密码写入 `conf/app.conf`。`SETExpirationObj`（`0xb0a5c0`）有 JSON.Marshal → Redis SET 的通用路径，此路径未看到额外加密；`GetLoginata`（`0xb0b040`）从 Redis 读取 JSON 并反序列化成 LoginData。

这要求保护配置、Redis、卷和备份，**不足以断言每个会话字段均为原始明文**：字段可能包含协议加密值，全部字段与所有写入方未逐项恢复。

服务配置监听 `0.0.0.0:9000`，pprof 默认配置 `0.0.0.0:9010`。外部是否可达取决于端口发布、网络及运行行为。即使已看到 pprof 鉴权，诊断端口仍不宜发布；内存剖析、信号触发 heap dump 和日志可能含敏感数据。证据：[入口脚本](evidence/entrypoint.sh)、[主程序伪代码](evidence/main-ghidra-selected.txt)。

### F6：整套部署存在 Docker 管理权限风险

`openchat-wx` 提交 `319fa8769fc871a303c423232d064effce70eb5c` 的服务器 Compose 第 220 行把 `/var/run/docker.sock` 挂给管理后端。若连接常规 rootful Docker daemon，控制管理后端可能进一步获得很强的宿主机控制能力；只读挂载 socket 文件不会自动把 API 变成只读。

挂载属于管理后端，不能误写成协议镜像默认挂载。建议整套试验放在独立虚拟机或专用主机，与已有业务 Docker daemon 分开。证据：[冻结 Compose](evidence/openchat-server-compose.yml)。

## main 与 libv08.so 的边界

```text
Go v08.init.0
  → NewLinuxLibrary("lib/libv08.so") → dlopen
Go v08.Rqtx(input)
  → linuxLibrary.Call("rqtx", ...) → dlsym → CGO 调用桥
libv08.so::rqtx
  → __init → 读取相对路径 rqtx.dat → fromHex → 内部数据缓冲区
  → rqtx_calc → 返回 32 位计算值
```

路径与函数名由精确字节、初始化指针、反编译和反汇编共同支持，未动态加载。库内 rqtx 计算使用输入指针及长度 `0x20`；不能据伪代码变量名就认定为 MD5。算法用途、全部输入约束和等价实现尚未确认。

库依赖 libstdc++、libgcc_s、libc，动态导入未见直接命名的 socket/connect/send/recv。这不排除间接调用、系统调用或主程序代为联网，更不能证明整套系统无远端依赖。

`./lib/key` 在 Go 的 vip08 中被读取、Base64 解码并打印；文件名不足以证明它是协议解密密钥。证据：[本地库报告](native-library-audit.md)、[动态加载代码](evidence/main-ghidra-followup.txt)、[精确字节](evidence/main-exact-literals.json)。

## 逆向可行性与未解决项

| 目标 | 结果或限制 |
| --- | --- |
| 查看配置、接口声明、部分函数和控制流 | 已做到；Go pclntab 提取 29,150 条函数记录，包含运行库和依赖，不代表恢复了全部业务代码 |
| 确认部分外联、鉴权、缓存和 main/so 边界 | 已做到，有地址、字节及工具输出 |
| 继续拆解 rqtx 与本地数据文件 | 有现实可行性；未做等价实现和测试向量验证 |
| 逐字还原 Go/C++ 源码、注释与构建仓库 | 不能由反编译保证 |
| 去掉全部远端依赖，独立实现登录和群收发 | 尚不能保证；还涉及其他流程、状态机、设备数据、远端返回值及版本变化 |
| 证明无后门、无账号风险、未来更新安全 | 本次静态分析不能作此保证 |

## 工具、覆盖范围与可信度

REA CLI 6.5.0 的 Go 检查读取到 `go1.24.13`、模块 wechatdll、CGO=1、linux/amd64。Evidence ID：`ev_c73e07ccf680fa64d0d106aa9ea668d4533329b0cfe5e2ed4be21f70165f8101`，见 [原始 Evidence](evidence/rea-go-evidence.json)，仅支持其具体元数据。

Windows REA Ghidra provider 的 native 打开路径不支持本 ELF，因此改用现有 WSL 与独立 Linux Ghidra 12.1.4。临时解包完整 OpenJDK 21 供分析器使用，没有执行目标程序或修改 MCP 配置。**独立 Ghidra 输出不是 REA Evidence**；rea-session-bundle.json 是空会话记录，不用于证明目标行为。

主程序自动分析达到 240 秒时限，属于部分分析，日志含 Go 类型恢复及函数边界告警。定点脚本成功导出选定函数，但不代表全程序分析完成。部分伪代码保留未知寄存器/调用和单地址函数体标记；关键字符串以原始字节和明确长度核对，关键关系补充 objdump/指针验证。未将伪代码近似表达当成运行验证。

重点覆盖已发现的启动、鉴权、Redis、UUID、OCR 和本地库路径，**不是所有函数与出站行为的穷尽证明**。原始样本位于 `C:\Temp\wechat-bot-research-20261023\wechat-ipad-static`，工具输出和复核步骤见 [证据索引](README.md)。

## 后续隔离验证与工程建议

1. 使用专用虚拟机/主机和固定 digest；协议容器不挂工作目录、SSH 密钥、个人文件或 Docker socket。若管理后端需要 Docker 控制权，也限定于这台隔离宿主。
2. 先核对配套服务版本、五项鉴权变量及签名协议，使用非真实微信数据检查启动和错误请求，再进入账号试验。
3. 协议 API、Redis 和诊断服务仅置于必要内部网络；限制 Redis 用户、数据库、卷与备份访问。非 root、drop capabilities、no-new-privileges、只读根目录和可写路径需要兼容测试，不能把未测参数当成可用配置。
4. 账号试验前记录 DNS、连接目的地、HTTP 参数，用出口策略控制第三方 OCR/UUID 服务；阻断后是否可用需要实测，不能预设可替换。
5. 群消息进入自己的队列/适配层，再调 DeepSeek；按群白名单和触发规则发送必要内容，密钥单独保存。协议进程天然能接触它处理的消息和会话，容器隔离消除不了这一信任边界。
6. 运行阶段验收包括签名错误被拒、日志无敏感 URL/会话、Redis 无公网暴露、记录出站目的地、断网重连、重启恢复和群消息闭环。

这些是后续建议；本目标未执行运行或账号试验，未产出号称可直接上线的配置。
