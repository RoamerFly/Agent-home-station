# 微信鲸鱼娘 AI 与网页管理端

**当前本机测试交付：0.9.2-platform-lifecycle-local，管理端 http://127.0.0.1:8081/**。支持微信后台收发、角色与群规则、图片/语音理解、分享卡片和视频处理、联网/MCP、声明式插件、本地 YuE 异步歌曲生成、上下文压缩及持久化发送。生成任务独立跟踪交付与回执，时长和格式由模型选参，后台验证权限与范围。优先阅读 [使用说明](USER-GUIDE.md)、[插件配置](PLUGINS.md)、[功能与验收边界](FEATURES.md)。

项目仓库：[RoamerFly/wechat-agent](https://github.com/RoamerFly/wechat-agent)（私有）。每次完成更新后执行相关验证、commit 和 push；协作更新使用独立分支和 PR。具体约定见 [AGENTS.md](AGENTS.md)。

源码与合成测试夹具纳入版本管理；API Key、微信会话、真实聊天导出、原始研究证据、模型、运行环境和构建产物保留在本机，不上传仓库。文档中的 `research/evidence/` 链接是本地验收索引，克隆仓库后不包含这些文件。GitHub Actions 只执行源码检查、测试和命令编译，不发送微信消息，不构建镜像，不发布 Release。

**本轮暂不构建、打包或导出 Docker 镜像。** 下文及 DEPLOY-WEB.md 的 Docker 0.3.0 说明保留为历史资料，旧测试包不包含本轮功能，不能作为当前交付版本。

**后续阶段也已跑通：DeepSeek 自动回复、文件传输助手 `/ai` 触发，以及“机器人测试群”只在真正 @ 小号时回复，均已由用户确认。使用、服务管理和 Docker 启用方法见 [机器人说明](BOT.md)。**

**第一阶段已于 2026-10-24 完成真实验证：测试小号在后台登录，向自己的文件传输助手发送“后台发送测试”，服务端接受后，用户在手机端确认看到了消息。** 客户端消息 ID 为 `788294973`，服务端消息 ID 为 `6809579812965061880`；只提交了一次，没有自动重发。见 [脱敏验收记录](research/evidence/linux-native-acceptance.json)。

当前采用公开源码 [thisnick/agent-wechat](https://github.com/thisnick/agent-wechat) 的独立 Linux Rust 协议客户端，固定提交 `7095ef01e0cd3d5b567bcb6970f379dcd88bbfa8`，配合本项目编写的 Go 发送 API。运行不需要微信桌面窗口、OpenClaw 或原闭源 `main/libv08.so`。这是复用公开源码的实现，不宣称所有协议算法都由本项目原创。

| 范围 | 当前证据 |
| --- | --- |
| 从源码构建 Linux 协议客户端 | Linux 源码编译通过，协议模块138项、核心模块186项测试通过 |
| 登录与真实文字发送 | 同一构建产物在 Windows 本机 WSL2 Ubuntu 中完成，手机确认收到 |
| Go HTTP API | 已接入已登录后台，`/healthz` 和 `/readyz` 均为 200；适配器测试覆盖排队、提交中、最终回执和禁止重发 |
| Docker 完整镜像 | `wechat-bot-linux:local` 已构建；容器启动、未登录 503、鉴权 401、白名单 403 通过 |
| 群聊 + DeepSeek | WSL Linux 后台真实收发通过，普通群消息静默、真实 @ 后自动回复；见 [验收](research/evidence/bot-deepseek-acceptance.json) |
| 尚未实测 | 完整 Docker 容器内重新授权和机器人回复、云服务器长期运行 |

真实消息通过 Linux 原生 API 发出；为了避免重复消息，没有再通过 Go API 重发一次。因此 Go API 的真实就绪、适配测试和真实原生发送分别记录，不混为同一条端到端测试。

## Windows 本机现有实例

本次后台在 WSL 发行版 `Ubuntu-New` 中运行，原生 API 只监听 `127.0.0.1:6174`，Go 发送 API 监听 `127.0.0.1:8787`，无微信桌面窗口。Windows 通过 WSL 的 localhost 转发访问；如果本机禁用了该转发，请在 WSL 内访问。

会话和令牌保存在 WSL 私有目录 `/home/fly/.local/share/wechat-bot-native-research-20261024`，不包含在源码或 Docker 镜像中。其中 `send-api-token` 是 Go API 令牌，`api-token` 属于原生 API；两者不同。不要把这些文件上传或发到聊天中。同一数据目录只能有一个协议后台持有写入权。

检查状态无需发送消息：

```powershell
Invoke-RestMethod http://127.0.0.1:8787/healthz
Invoke-RestMethod http://127.0.0.1:8787/readyz
```

现在已由三个 WSL 用户服务托管，日常查看和重启方式见 [BOT.md](BOT.md)。下面是早期手动启动方式，仅在先停止对应服务后用于排查，不要与服务同时运行：

```bash
# 终端 1：协议后台，使用原有数据目录恢复状态
/mnt/e/A-Importance/wechat-bot/dist/linux-amd64/wx-linux-native \
  --data-dir /home/fly/.local/share/wechat-bot-native-research-20261024 \
  --listen 127.0.0.1:6174 serve

# 终端 2：从本地私有文件读取 API Token，不在命令行显示它
python3 /mnt/e/A-Importance/wechat-bot/research/scripts/serve_linux_api.py \
  --data-root /home/fly/.local/share/wechat-bot-native-research-20261024 \
  --account background-test-20261024
```

WSL 用户服务已启用，但尚未创建 Windows 开机启动 WSL 的任务；关闭 WSL 会停止它。持久部署建议使用下面的 Docker 配置。

## Ubuntu / Docker 从源码部署（amd64）

需要 Docker Compose、Git 和本项目源码。协议客户端另行检出固定提交；不要把含会话的私有目录用作构建上下文。

```bash
# 在本项目根目录执行；上游源码放在项目目录外。
git clone https://github.com/thisnick/agent-wechat.git ../agent-wechat
git -C ../agent-wechat checkout --detach 7095ef01e0cd3d5b567bcb6970f379dcd88bbfa8
git -C ../agent-wechat apply "$PWD/research/patches/native-client-local-integration.patch"
docker build -f "$PWD/research/docker/Dockerfile.linux-native" \
  -t wechat-linux-native:research-7095ef0 ../agent-wechat

export WECHAT_API_TOKEN="$(openssl rand -hex 32)"
export WECHAT_NATIVE_ACCOUNT=background-test
export WECHAT_ALLOWED_RECIPIENTS=filehelper
docker compose -f compose.linux-native.yaml up -d --build
```

首次编译 Rust 需要几分钟和足够内存；构建并发限制为 4。基础镜像固定 digest，Cargo 使用 `--locked`。以上是早期协议验证部署路径，不是本轮管理端的最终交付包；当前 0.6.0 功能以本机运行项目和 USER-GUIDE 为准。本轮未构建或导出镜像。原生客户端必须应用语音播放补丁，避免旧版本将取消标记与 SILK 格式字段写反。Rust/Debian 基础镜像通过 `mirror.gcr.io` 获取。

首次创建账号并授权（新数据卷初始 `/readyz` 为 503，属于预期）：

```bash
docker compose -f compose.linux-native.yaml exec wechat-bot \
  wx --server http://127.0.0.1:6174 --token-file /data/api-token \
  accounts create background-test --alias background-test

docker compose -f compose.linux-native.yaml exec wechat-bot \
  wx --server http://127.0.0.1:6174 --token-file /data/api-token \
  --account background-test auth login --strategy qr --force-qr \
  --qr-refresh manual --max-refreshes 0 --timeout 300 --qr-output /data/login.png
```

登录命令等待期间，在另一终端复制二维码后打开，用测试小号扫码并完成手机上的全部确认：

```bash
mkdir -p secrets
docker compose -f compose.linux-native.yaml cp wechat-bot:/data/login.png ./secrets/linux-login.png
curl --fail http://127.0.0.1:8787/readyz
```

二维码要等生成后再复制；过期后需要显式启动新登录。日常重启保留 `native-data` 卷，不重复创建账号；不要执行 `down -v` 删除会话。每次进入新 shell 都需加载同一组环境配置；若用 `.env` 保存 API Token，将其权限设为 600，勿提交版本库。原生令牌和会话由后台在卷内生成，无需手工导出。

Compose 仅发布宿主机 `127.0.0.1:8787`；6174 不向外发布。进程使用 UID 10001、只读根目录、独立临时目录和持久卷，不挂载 Docker socket。入口脚本管理两个子进程，收到停止信号会同时退出；子进程异常退出后由 Compose 重启整个服务。这不替代长期运行和掉线恢复测试。

## 发送接口

```http
POST /v1/messages/text
Authorization: Bearer <WECHAT_API_TOKEN>
Content-Type: application/json

{"recipient":"filehelper","text":"要发送的文字"}
```

实际发送命令（会产生一条新微信消息；不需要为已完成的验收再次执行）：

```bash
curl --fail-with-body http://127.0.0.1:8787/v1/messages/text \
  -H "Authorization: Bearer $WECHAT_API_TOKEN" -H 'Content-Type: application/json' \
  --data '{"recipient":"filehelper","text":"新的后台消息"}'
```

也可以运行 `wechat-send send -to filehelper -text "新的后台消息"`，CLI 从环境读取同一 API Token。`recipient` 必须在逗号分隔的 `WECHAT_ALLOWED_RECIPIENTS` 白名单内；空名单拒绝所有发送。文字限制为 4096 UTF-8 字节，请求体最多 16 KiB。

| HTTP 状态 | 含义 |
| --- | --- |
| 202 `accepted` | 已取得目标一致、客户端/服务端消息 ID 有效的服务端接受回执；不表示对方已读 |
| 401 / 403 | API Token 错误 / 收件人不在白名单 |
| 503 `transport_not_ready` | 后台不可用、账号未登录或登录状态不可确认 |
| 422 `message_rejected` | 后台明确拒绝发送 |
| 504 `outcome_unknown` | 超时、连接中断或回执不能确定；原消息可能已经发出，**不要自动重试** |

Go 适配器只连接显式配置的本机 loopback 地址，禁止代理和重定向；一次请求只提交一次发送意图，随后只查询状态。排队和 `submitting` 不是最终成功。Go 接口目前未提供调用者可恢复的幂等键，调用方也不得自行把超时当作可安全重试。

## 单独编译 Go API

```powershell
go test ./...
go vet ./...
go build -trimpath -o dist/windows-amd64/ ./cmd/...
```

选择已验证的 Linux 后端必须显式设置：

```text
WECHAT_TRANSPORT=linux-native
WECHAT_NATIVE_URL=http://127.0.0.1:6174
WECHAT_NATIVE_TOKEN_FILE=<本机可读的原生 api-token 文件路径>
WECHAT_NATIVE_ACCOUNT=<原生后台的账号 ID>
WECHAT_API_TOKEN=<至少 24 字节的独立 API Token>
WECHAT_ALLOWED_RECIPIENTS=filehelper
```

Go API 本身可在 Windows 编译运行；原生协议后台目前采用 Linux，Windows 本机通过 WSL2 或 Docker Desktop 运行。`WECHAT_TRANSPORT` 未设置时保留旧 iPad 实验后端；它授权未通过，不是当前成功路线。原 `Dockerfile` / `compose.yaml` 也保留用于该历史实验；正常使用请选择 `compose.linux-native.yaml`。

## 来源与边界

实现入口见 [linuxnative 适配器](internal/linuxnative/sender.go)、[API](internal/httpapi/httpapi.go)、[Docker 部署](compose.linux-native.yaml)。验收、构建哈希及历史逆向结论见 [research](research/README.md)。旧 iPad 实验说明归档在 [README.ipad-experimental.md](README.ipad-experimental.md)，其中“未完成发送”指该历史路线。

复用源码固定提交和 Cargo.lock，但固定版本不保证微信服务端永远兼容。上游 fresh-profile 登录标注为实验功能；本次成功只覆盖这个测试账号和这条消息。后续多轮上下文、媒体收发、图片理解和网页管理已见 0.5.0 的功能与验收文档；本段仅记录早期原生登录实验，跨机器会话迁移仍未验收。部署到云端后仍需在目标机器授权和验收。

这是公开源码依赖，不据此推断整个上游具有统一的宽松许可证：本次检出的根目录未见统一 LICENSE，部分组件自带 NOTICE/LICENSE。内部测试与后续对外分发应分别核对对应源码及依赖的许可；镜像不捆绑本次测试账号的凭据。
