# DeepSeek 自动回复机器人

> 本文主要描述早期独立命令行机器人（未启用网页角色运行时）。当前 0.5.0 网页管理版本的多轮记忆、视觉理解、持久化队列与可配置延迟补发，以 [USER-GUIDE.md](USER-GUIDE.md) 和 [FEATURES.md](FEATURES.md) 为准；下方早期“不存正文、不重试”取舍不代表当前网页版本。

**当前已升级网页管理：请优先使用 [网页版部署与本机管理说明](DEPLOY-WEB.md)。** 本机网页 `http://127.0.0.1:8081` 已接管原机器人，`wechat-bot-web.service` 管理网页和自动回复。原 `wechat-bot-ai.service` 已禁用，不应与网页管理器同时启动。下文是升级前的独立 CLI 方式，尤其不要再运行旧的全套安装脚本覆盖当前服务。

**2026-10-24 已真实跑通：文件传输助手 `/ai` 触发、DeepSeek 回答、微信自动回复；“机器人测试群”普通消息不回复，真正 @ 小号后回复。两种回复均取得微信接受回执，用户也确认收到了。** 见 [脱敏验收记录](research/evidence/bot-deepseek-acceptance.json)。

当前架构是 Linux 原生协议后台 + Go 机器人 + DeepSeek HTTP API，没有 OpenClaw 和桌面微信依赖。默认模型为 `deepseek-flash`，按照 [DeepSeek 官方 Chat Completions 文档](https://api-docs.deepseek.com/api/create-chat-completion/) 调用；`DEEPSEEK_MODEL` 可显式调整。

## 现在怎样使用

本机已配置的范围仅为测试小号的文件传输助手和“机器人测试群”：

- 文件传输助手：手机发 `/ai 你的问题`，`/ai` 后需要一个空格。
- 机器人测试群：用另一个群成员账号，真正选择 @ 小号，再输入问题。手工打出昵称不算真正 @。
- 回复以 `[AI]` 开头。普通群消息、未授权会话、图片、语音、系统消息、自身回复均不触发模型。
- @ 全体成员本身不触发；只有结构化标记确认 @ 到小号本人才触发。

目前每次只把**这一条匹配触发消息的文字**发送给 DeepSeek，尚未接入多轮上下文或工具执行。原生后台按其协议实现保存微信消息；Go 机器人的额外状态只保存处理游标和消息 ID 哈希，不保存另一份聊天正文、模型答案或密钥。日志也只记录事件类型。

## Windows / WSL 服务管理

已在 `Ubuntu-New` 的用户服务管理器中安装并启用：

| 服务 | 作用 |
| --- | --- |
| `wechat-bot-native.service` | 持有唯一微信协议会话，监听 127.0.0.1:6174 |
| `wechat-bot-api.service` | 手动发送 HTTP API，监听 127.0.0.1:8787 |
| `wechat-bot-ai.service` | 读取事件、调用 DeepSeek、在机器人白名单内回复 |

在 Windows PowerShell 中运行：

```powershell
# 三项应均为 active
wsl -d Ubuntu-New -- systemctl --user is-active wechat-bot-native wechat-bot-api wechat-bot-ai

# 暂停 / 恢复自动回复，保留协议登录和手动发送 API
wsl -d Ubuntu-New -- systemctl --user stop wechat-bot-ai
wsl -d Ubuntu-New -- systemctl --user start wechat-bot-ai

# 只重启机器人 / 查看日志
wsl -d Ubuntu-New -- systemctl --user restart wechat-bot-ai
wsl -d Ubuntu-New -- journalctl --user -u wechat-bot-ai -n 30 --no-pager

Invoke-RestMethod http://127.0.0.1:8787/readyz
```

服务使用 WSL 私有目录 `/home/fly/.local/share/wechat-bot-native-research-20261024` 内的程序副本和凭据，不再依赖对话中的临时终端进程。服务单元位于 `/home/fly/.config/systemd/user/`，私有 `service-launch.py` 配置现有账号和已授权群 ID。同一状态目录不能同时启动第二个协议后台；机器人也有进程锁。

WSL 发行版启动并启动用户服务管理器后，已启用的服务会运行；**这不是 Windows 开机自动启动 WSL 的计划任务**。`wsl --shutdown`、Windows 关机或休眠仍会中断运行。

API Key 的设置输入为项目中的 `secrets/deepseek-api-key.txt`，运行副本为 WSL 私有目录中的 `deepseek-api-key`，权限 600。更新 Windows 中的 Key、重新编译程序后，可运行安装脚本更新副本和服务：

```powershell
# 仅用于当前测试实例，会短暂重启三个服务，沿用原登录和机器人进度。
wsl -d Ubuntu-New -- python3 /mnt/e/A-Importance/wechat-bot/research/scripts/install_local_services.py
```

脚本不会扫码、发送测试消息或创建 Windows 开机任务。

## Ubuntu / Docker 启用机器人

先按照 [README 的源码部署步骤](README.md) 构建原生镜像，设置 `WECHAT_API_TOKEN`、`WECHAT_NATIVE_ACCOUNT` 并完成账号登录。机器人通过单独的 Compose 覆盖文件启用，基础发送服务不会默认调用模型。

将 API Key 单独一行保存到项目的 `secrets/deepseek-api-key.txt`。此文件不会进入镜像。在 Linux 宿主上，文件需能被容器 UID 10001 读取：

```bash
# 只调整这个密钥文件，不调整项目其他文件的归属。
sudo chown 10001:10001 secrets/deepseek-api-key.txt
sudo chmod 600 secrets/deepseek-api-key.txt

# 先仅启用文件传输助手；群聊请填真实群 ID，不能直接填群名。
export WECHAT_BOT_ALLOWED_THREADS=filehelper
export DEEPSEEK_MODEL=deepseek-flash
docker compose -f compose.linux-native.yaml -f compose.bot.yaml up -d --build
docker compose -f compose.linux-native.yaml -f compose.bot.yaml logs --tail 30 wechat-bot
```

Windows Docker Desktop 的宿主文件权限映射不同，不运行上述 Linux `chown`。Key 以只读 secret 挂载；如果容器权限不匹配，入口脚本会报告文件不可读并退出，不会把 Key 放进命令参数。

先查询群名对应的 ID，然后加入机器人白名单：

```bash
docker compose -f compose.linux-native.yaml exec wechat-bot \
  wx --server http://127.0.0.1:6174 --token-file /data/api-token \
  --account "$WECHAT_NATIVE_ACCOUNT" groups --search '机器人测试群'

# 以查到的 roomId 替换示例，再执行上面的双 Compose 启动命令。
export WECHAT_BOT_ALLOWED_THREADS='filehelper,实际群ID@chatroom'
```

机器人白名单 `WECHAT_BOT_ALLOWED_THREADS` 与手动发送 API 的 `WECHAT_ALLOWED_RECIPIENTS` 分开配置。机器人在自己的白名单内通过原生 API 回复，二者不会相互扩大范围。

首次启用时，机器人等待登录就绪，然后把当前事件末尾保存为起点；**开始监听前的消息不会补答**。确认日志出现 `bot_started_history_skipped` 或 `bot_resumed_saved_cursor` 后再发新测试消息。

## 单独运行 Go 机器人

构建 `go build -o dist/windows-amd64/wechat-bot.exe ./cmd/wechat-bot` 或面向 Linux 编译。配置如下：

| 环境变量 | 含义 |
| --- | --- |
| `WECHAT_NATIVE_URL` | 原生 API，仅允许 HTTP 本机 IP，例如 http://127.0.0.1:6174 |
| `WECHAT_NATIVE_TOKEN_FILE` | 原生后台的本地 Token 文件路径 |
| `WECHAT_NATIVE_ACCOUNT` | 原生后台账号 ID，使用真实 ID，不用昵称 |
| `WECHAT_BOT_STATE_DIR` | 专用持久目录，不同账号不能混用 |
| `WECHAT_BOT_ALLOWED_THREADS` | 逗号分隔的会话 ID，空值拒绝启动 |
| `DEEPSEEK_API_KEY_FILE` | 单独一行 Key 的本地文件路径 |
| `DEEPSEEK_BASE_URL` | 默认 https://api.deepseek.com；仅接受 HTTPS，禁止重定向和代理 |
| `DEEPSEEK_MODEL` | 默认 deepseek-flash |
| `WECHAT_BOT_PREFIX` | 私聊/文件传输助手触发前缀，默认 `/ai ` |

`wechat-bot -check-model` 只发一条固定的模型连接测试提示，不发送微信消息；它会产生一次模型 API 调用。机器人不执行 shell、不调用工具，也不把密钥作为提示词内容。

## 失败和恢复行为

机器人串行处理，同一会话默认冷却 5 秒；超过 2 分钟的消息不回复，避免重启后补发陈旧回答。回复最多 4096 UTF-8 字节，超出会在字符边界截断。DeepSeek 超时、拒绝、空答案或输出未完成时，不把错误消息发到群里。

**模型调用或微信发送不会自动重试。** 机器人先保存进度和去重哈希，再调用外部接口：若随后崩溃或失败，这条问题可能没有回答；它也不会在重启后重发一条可能已经成功的回复。这是当前的明确取舍，不保证每条输入都恰好回复一次。

事件游标过期、损坏或属于其他账号/后台时，机器人停止并记录错误，不静默重置后重放历史。需要人工核对后恢复，不要直接删除进度文件当作通用修复。读取故障也可能使进程退出，由服务管理器重启；发送结果未知时仍不重放。

已验证 Windows 和 Linux 的过滤、去重、锁、重启不重发、异常回执、模型响应边界及凭据不随重定向转发。Docker 镜像已包含机器人，并检查“等待首次登录”的启动流程。**完整 Docker 容器内真实授权与 AI 群回复、云服务器长期运行仍未实测**；本机 WSL 的真实成功不能代替这些部署验收。
