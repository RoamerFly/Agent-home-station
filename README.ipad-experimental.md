> 历史实验快照：以下为 Linux 路线实测成功之前的 iPad 逆向状态。当前可用路线和验收结果以 [README.md](README.md) 为准。iPad 授权仍未通过，不用于已验证发送路线。

# 微信后台文字发送原型

这是从源码构建的 Go 后台服务，面向 Windows 本机试验和 Ubuntu/Docker。当前已经实现文字消息编码、业务封包、纯 Go RQTX 解释器、候选 MMTLS 短连接传输及发送 API，能够编译和启动。

**截至 2026-10-24，自写协议已取得真实二维码、扫码确认状态 `2` 和真实设备令牌；login-4 携带已恢复的完整设备附加信息提交授权后，微信返回 `-106`，正文明确提示“应用版本过低，请升级至最新版本后再登录”。尚未生成可发送会话，真实文字发送仍未完成。** 该提示不能单独证明数值版本是唯一根因，也不能据此认定账号被封。没有会话时服务会返回 HTTP 503 `transport_not_ready`。合成回执只证明本地代码链路。

## 当前状态

| 部分 | 状态 |
| --- | --- |
| 后台 HTTP API、Token、收件人白名单 | 已实现并测试 |
| 文字 protobuf 编码及回执解析 | 已按原样本描述符实现；与另一 protobuf 运行时的测试夹具一致 |
| 业务 AES-CBC 封包、CRC、长度检查 | 已实现；部分协议细节仍需真实互通核对 |
| RQTX | 自写有界 Go VM，132 组隔离原库输出对照通过 |
| MMTLS 初始握手及短连接记录层 | 真实二维码和轮询链路通过；长期服务端身份签名公钥仍未固定，保持实验状态 |
| 已有会话加载 | 自定义 JSON 格式，显式启用实验协议；不是原服务 Redis 文件的直接替代 |
| Windows/Linux 二进制、Docker | 已构建；后台启动和无会话拒绝发送验证通过 |
| 二维码、扫码确认 | 真实微信端验证通过，手机两次确认完成，poll 返回状态 2 |
| 设备令牌 fpinitnl | 自写 Go 请求已取回真实设备令牌；持久设备资料与扫码身份分开保存 |
| version-8 设备附加信息 | 编码、zlib、表驱动加密与完整主体已恢复，独立编码对照通过；授权互通待验证 |
| newinit 初始化 | CGI139 封包、protobuf、单次请求、响应归档和同步键保存已实现；待真实会话验证 |
| 授权生成会话 | login-4 已完成扫码并提交一次完整候选授权，微信以“应用版本过低”拒绝；没有生成账号会话 |
| 真实微信文字发送 | 尚未验证 |

运行时不会加载原闭源 `main` 或 `libv08.so`。`internal/rqtx/program.hex` 是从固定样本中恢复并嵌入的**不透明 VM 字节码数据**；设备附加信息还使用显式指定的 `08sae.dat` **不透明加密表**。目前保留这两类原始数据依赖，尚未恢复成无原始数据依赖的完整算法。样本与恢复依据见 [research](research/README.md)。

本次还只读提取了上游 2026-10-23 发布的 `v3.2.3/latest` 镜像。iPad 默认版本 `402668320`（`0x18003b20`）、设备类型和 bootstrap 公钥仍与旧样本相同；运行时允许传入版本，但未找到经过验证的新配置。详见 [升级证据](research/version-upgrade-evidence.md) 和 [当前验收状态](research/acceptance-status.md)。在配置或编码兼容性有新证据前，单纯重复扫码不能验证修复。

随后从公开 PadPro 发布物中读到了候选版本 `402669099`（`0x18003e2b`），已实现显式升级和一次真实对照：设备身份保持一致，新设备令牌返回 `0`，login-5 扫码确认状态 `2`；但授权仍返回 `-106` 和同样的升级提示。见 [升级预检](research/evidence/upgrade-preflight-20261024.json) 与 [升级授权结果](research/evidence/upgrade-authorization-20261024.json)。候选配置保留原传输和 v8 证明实现，只改变版本，不代表完整移植 PadPro 协议。

## 本机运行

需要 Go 1.24+；当前应用代码仅使用标准库，不需要 Redis、Python 或微信桌面窗口。

```powershell
go test ./...
go build -trimpath -o dist/windows-amd64/ ./cmd/...

# 生成仅在本机使用的随机 API Token，不打印它。
$env:WECHAT_API_TOKEN = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
$env:WECHAT_ALLOWED_RECIPIENTS = "filehelper"
$env:WECHAT_LISTEN = "127.0.0.1:8787"

.\dist\windows-amd64\wechat-sendd.exe
```

需要隐藏后台窗口时，可在设置环境变量后启动：

```powershell
$botProcess = Start-Process -FilePath ".\dist\windows-amd64\wechat-sendd.exe" -WindowStyle Hidden -PassThru
# 完成本机试验后停止自己启动的进程：
# Stop-Process -Id $botProcess.Id
```

`GET http://127.0.0.1:8787/healthz` 检查进程；`GET /readyz` 检查是否已经配置发送会话。ready 仅表示可尝试实验协议，不能证明微信端仍认可会话。当前未配置会话时，health 返回 200、ready 返回 503，这是预期结果。

## 准备文字请求包

这条命令只在本地生成 protobuf 文件，不连接任何微信服务器；为防止误覆盖，输出文件必须不存在。

```powershell
.\dist\windows-amd64\wechat-send.exe prepare -to "filehelper" -text "后台发送测试" -out ".\dist\text-request.pb"
```

## 发送接口

会话建立并经过互通验证后，调用：

```http
POST /v1/messages/text
Authorization: Bearer <本机 API Token>
Content-Type: application/json

{"recipient":"目标wxid或群ID@chatroom","text":"后台发送测试"}
```

接收者必须出现在 `WECHAT_ALLOWED_RECIPIENTS` 中（逗号分隔）；空白名单拒绝所有接收者。项目目前将文字限制为 4096 UTF-8 字节，请求体最多 16 KiB，这是本服务的策略限制，不是对微信协议上限的判断。

也可以使用同一环境变量下的 CLI：

```powershell
.\dist\windows-amd64\wechat-send.exe send -to "filehelper" -text "后台发送测试"
```

只有通过 MMTLS 认证、业务解包以及对应收件人/client ID 的回执检查，API 才返回 202 `accepted`，消息 ID 用十进制字符串表示以保留 uint64 精度。accepted 表示服务端接受的候选回执，不表示送达或已读。

没有会话返回 503；明确的微信错误返回 422；网络超时、回执损坏等返回 `outcome_unknown`。**结果未知时不要自动重试**，原请求可能已被处理。服务和 CLI 均不自动重发。

## 会话输入

`WECHAT_SESSION_FILE` 可指定只读会话 JSON，结构定义在 [sender.go](internal/native/sender.go)。需要 UIN、15 字节 cookie、16 字节会话密钥、登录 salt、公用客户端版本、MMTLS PSK 和票据等材料。字节数组采用 Base64。

这是本项目的自定义交换格式，不能把普通登录二维码 UUID、网页版 cookie 或桌面微信数据库密钥当作这些材料。当前授权未完成，先使用无会话模式检查构建与后台服务。不要在聊天中发送会话文件或密钥。

## 实验登录工具

`wechat-login` 与发送服务一样从本仓库 Go 源码构建。它直接请求微信域名，不请求第三方协议服务或在线二维码渲染服务。设备 ID 会保存在本地并复用。

已增加 `upgrade` 子命令，把已存设备资料迁移为新文件，保留设备 ID、UUID 和文件统计，同时清除旧设备令牌。候选 JSON 记录版本来源、原始样本哈希和实验范围。二维码、轮询、设备令牌、授权及输出会话现在沿用同一版本配置，避免仅改一个硬编码位置而其余请求仍携带旧值。例如：

```powershell
.\dist\windows-amd64\wechat-login.exe upgrade -from secrets/device-profile.session.json -out secrets/device-profile-candidate.session.json -candidate configs/ipad-402669099-experimental.json
```

该命令只做本地迁移，不连接微信，也不表示候选版本已经可登录。当前保存的 `402669099` 对照已明确授权失败，不应将重复扫码当作修复。

完整实验路径需要先生成一次持久设备资料，并请求真实设备令牌：

```powershell
.\dist\windows-amd64\wechat-login.exe profile -state secrets/device-profile.session.json
.\dist\windows-amd64\wechat-login.exe fingerprint -state secrets/device-profile.session.json -tables research/samples/08sae.dat.bin
.\dist\windows-amd64\wechat-login.exe qr -state secrets/pending.session.json -image secrets/login.png -profile secrets/device-profile.session.json
.\dist\windows-amd64\wechat-login.exe poll -state secrets/pending.session.json -wait 5m
# 只有状态2之后才执行一次授权；从qr记录的profile加载同一设备与真实令牌。
.\dist\windows-amd64\wechat-login.exe authorize -state secrets/pending.session.json -session secrets/wechat.session.json -tables research/samples/08sae.dat.bin
# 授权实际成功并保存会话后，再请求一次初始化。
.\dist\windows-amd64\wechat-login.exe initialize -session secrets/wechat.session.json
```

生成器恢复的是该客户端的虚拟设备资料规则，保留独立 profile 的 UUID、统计值和型号；二维码的原始 16 字节 ID 与其十六进制文本另行固定保存，不能换成 profile 的哈希 ID。`profile` 输出必须为新文件；日常使用不应重新生成资料。`fingerprint` 不自动重试，已成功的响应可用显式 `-refresh` 刷新一次并保留历史；未知失败不会直接重复请求。私有目录包含令牌和会话材料，不应提交或发到聊天中。

```powershell
.\dist\windows-amd64\wechat-login.exe qr -state secrets/pending.session.json -image secrets/login.png
# 打开 login.png 后立即开始等待，再用测试小号扫码并完成手机上的全部确认。
.\dist\windows-amd64\wechat-login.exe poll -state secrets/pending.session.json -wait 5m
```

`qr_status: 2` 只表示拿到扫码确认和临时授权材料。`poll` 不会自动提交授权；普通等待状态会以三秒间隔查询，错误立即停止，最长允许等待十分钟。图片和状态文件必须是新路径，设备文件默认复用 `secrets/device.bin`。

`authorize -state <状态文件> -session <新会话文件>` 是尚未完成互通的授权实现。指定设备令牌 profile 和表文件后，会从源码生成 version-8 附加信息；不要把离线编码测试视为授权成功。工具在请求前持久化“已尝试”标记，同一状态文件不再提交第二次授权，网络失败也不会自动重试。

授权返回被解密后，工具会先保存 `<会话文件>.auth-exchange.session.json` 私有恢复记录，再解析会话；这样出现编码或字段兼容问题时，可在本地继续恢复而不重复请求。该文件含敏感密钥和响应材料，须与会话文件同样保护，不应上传或显示其内容。

`initialize` 使用授权后的同一会话发出一次 CGI139 请求，在发送前持久化 in-flight 标记，收到响应后保存 `<会话文件>.newinit-response.session.json`、当前/最大同步键及返回命令。返回命令目前保留原始数据供后续联系人/消息同步解析；不表示已完成本地联系人索引或全量同步。`ContinueFlag` 仅原样输出，依据当前已恢复调用路径不自动循环。未知结果保留标记并禁止自动重发；`initialization_verified` 仅表示这一次响应通过校验和保存，不能替代真实文字发送验收。

2026-10-24 第一次真实尝试中，微信授权返回 `-106`。样本枚举将其命名为 `MM_ERR_BLOCK_BY_SPAM`，但这个返回码本身不足以区分设备字段问题、协议校验或账号风控，不能据此断定封号。第一次授权没有保留响应说明，不猜测更具体原因。脱敏记录见 [真实登录尝试](research/evidence/login-live-attempt-20261024.json)。之后已恢复字段 24 的 version-8 `WCExtInfo` 生成链，并真实取得设备令牌，见 [令牌验证](research/evidence/fingerprint-live-20261024.json)；字段 23 在样本这条路径同样 absent。具体映射见 [授权字段审计](research/auth-field-audit-20261024.md)、[设备主体重建](research/spam-v8-reconstruction.md)。完整授权、会话初始化与单条文字发送仍需真实验收。

## Ubuntu / Docker

在项目根目录设置 API Token 和白名单，然后运行：

```bash
export WECHAT_API_TOKEN="$(openssl rand -hex 32)"
export WECHAT_ALLOWED_RECIPIENTS="filehelper"
docker compose up -d --build
```

Compose 仅向宿主 `127.0.0.1:8787` 发布端口，使用非 root 用户、只读根目录和 dropped capabilities。Dockerfile 已固定构建与运行基础镜像 digest。当前构建的本地镜像名为 `wechat-sendd:dev`，Compose 默认会自行命名镜像；无会话均保持未就绪。

需要配置会话时，参照 compose.yaml 的注释启用只读文件挂载。容器内路径由 `WECHAT_SESSION_FILE` 指定。镜像也包含 `/usr/local/bin/wechat-login`，可用 `--entrypoint` 运行；生成登录文件时需单独挂载有写权限的私有目录。生产部署前仍需完成授权、会话恢复和真实发送验证。

## 验证与边界

- `go test ./...`、`go vet ./...`：应用测试和静态检查；research 为独立模块，不混入冻结的外部源码证据。
- `internal/wechatwire/testdata`：依据样本中的 mm.proto，用 Google Python protobuf 生成的独立编码对照。
- `internal/rqtx/testdata/oracle-vectors.json`：132 个本地隔离调用原库获得的计算输入/输出，不含账号数据。
- `internal/native/integration_test.go`：合成 MMTLS peer 的本地发送链路，包含认证失败和损坏回执检查；没有使用真实微信服务器。
- [send-smoke.json](research/evidence/send-smoke.json)：Windows 程序和 Linux Docker 容器的后台启动、鉴权、无会话拒绝发送结果。
- [登录恢复依赖](research/login-reconstruction-plan.md)：继续实现扫码登录所需的协议步骤。

第一阶段目标仍在推进。编译成功、原库计算向量一致和合成 peer 测试均不能替代真实小号扫码后的发送验收。
