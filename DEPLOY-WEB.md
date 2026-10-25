# 网页版部署：导入镜像后直接打开控制台

> 当前为开发草稿。用户已要求先完善功能、暂缓导出；已有测试包不视为正式发布。下文保留为功能完成后的部署说明。

镜像包含网页、配置管理、AI 机器人和 Linux 原生微信协议后台。**启动一个容器就会自动启动网页及协议后台**，不需要再安装 Go、Rust、Node.js、微信桌面客户端、Redis 或 OpenClaw。

## Linux 服务器最快启动（x86_64 / amd64）

将 `wechat-bot-web_0.3.0_linux-amd64.tar.gz` 上传到已安装 Docker 的 Linux 服务器，然后执行：

```bash
docker load -i wechat-bot-web_0.3.0_linux-amd64.tar.gz
docker run -d --name wechat-bot --restart unless-stopped \
  --network host -v wechat-data:/data wechat-bot-web:0.3.0
```

浏览器打开 `http://服务器IP:8080`。首次管理密码由容器随机生成，查看它：

```bash
docker exec wechat-bot cat /data/admin/admin-token
```

在网页输入这个管理密码。无需把 API Key 写进启动命令：登录后在页面配置即可。若服务器有防火墙或云安全组，需要按自己的访问范围开放 TCP 8080。公网长期使用建议在前面配置 HTTPS；也可设置 `WECHAT_ADMIN_LISTEN=127.0.0.1:8080` 并通过 SSH 隧道访问。

此启动方式使用 Linux 的 host 网络，网页监听 8080，原生 API 仅监听服务器 loopback 的 6174；这两个端口需未被其他实例占用。不需要 `-p`。默认 Compose 也采用同一方式。本次已在 Ubuntu 原生 Docker Engine 的 host 网络中验证二维码及网页；如果改用 bridge 网络，应另行核对服务器出口是否允许原始微信协议流量。

镜像目前为 **Linux amd64**，不能据此保证 ARM64 机器原生可用。镜像文件不包含本机测试账号、微信会话、API Key 或管理密码；服务器上首次使用需要自行扫码授权。

## 使用随包的 Compose

同一目录的 `compose.web.yaml` 提供持久卷、只读根目录、非 root 用户、临时目录和进程退出重启配置，不要求预先填写环境变量：

```bash
docker compose -f compose.web.yaml up -d
docker compose -f compose.web.yaml exec wechat-bot cat /data/admin/admin-token
```

默认监听宿主的 8080 端口。改端口或仅本机访问：

```bash
WECHAT_WEB_PORT=18080 WECHAT_WEB_BIND=127.0.0.1 docker compose -f compose.web.yaml up -d
```

反向代理提供 HTTPS 时，将 `WECHAT_ADMIN_ORIGIN` 设为浏览器实际访问的完整 origin，例如 `https://bot.example.com`，不带结尾斜线。代理应保留真实 Host。这样浏览器会使用 Secure Cookie，修改配置的请求也会核对该 origin。

## 网页里的配置顺序

1. **微信登录**：生成二维码，用自己的测试小号扫码，完成手机上的全部确认。二维码只在手动点击时生成，过期后手动重新开始。
2. **服务商与角色**：选 DeepSeek，或 OpenAI 兼容接口；填写基础 API 地址、模型和 API Key。基础地址示例为 `https://api.deepseek.com` 或 `https://你的服务/v1`，不需要再附加 `/chat/completions`。
3. **角色设定**：选择助手、客服、讲解员模板，或编辑系统提示词；可设置输出 Token 上限、冷却时间和消息有效期。保存并应用。
4. **回复规则**：启用文件传输助手，或填写私聊用户 ID；群聊可以按名称搜索加入白名单，也可手动填群 ID。每个群独立选择“仅 @ 小号”或“回复全部消息”，并可追加触发前缀。
5. **启动自动回复**：概览页点击启动。首次启动跳过旧消息，看到运行中后再发送新问题。

API Key 只允许写入，不会回显；留空保存保留原 Key，清除会停止自动回复。配置存在持久卷的私有文件里，Key 不在浏览器 localStorage、镜像层或正常日志中。模型连接测试会产生一次 API 调用，不发送微信消息。

默认文件传输助手和允许的私聊使用 `/ai ` 前缀；群聊默认仅对真正 @ 小号的消息回复，手工输入昵称不算 @。选择“回复全部消息”会让该群符合前缀规则的普通文字也触发模型，请只对希望启用的群设置此项。

每次只发送当前触发的文字给模型，尚未加入多轮记忆、图片语音理解或工具执行。机器人串行处理；模型或微信发送失败不自动重试，重启也不会补发一条可能已经成功的回复。因此故障时可能少答一条，而不是保证每条输入都回复。

## 停止、升级和备份

- 暂停回复：网页点击“停止自动回复”，保留微信登录和网页。
- 停止整个容器：`docker stop wechat-bot`；再次启动用 `docker start wechat-bot`。
- Compose 停止：`docker compose -f compose.web.yaml down`，不要加 `-v`，否则会删除持久会话和配置。
- 新版本升级：先保存新的镜像，再停旧容器，用同一个 `/data` 持久卷重建；不要让两个协议后台同时写一个数据卷。
- 备份：先停容器，再备份整个数据卷；备份包含微信会话、Key 和管理密码，应按凭据保护。把镜像导出文件当作备份不会保存运行数据。

网页有独立管理员登录、HttpOnly/SameSite Cookie、配置修改 CSRF 校验、登录速率限制和页面内容安全策略。它是单管理员、单微信账号控制台；不提供多租户权限管理或任意系统命令执行。

## Windows 本机已有实例

当前已登录的小号已接入 WSL 网页后台：`http://127.0.0.1:8081`。原来的独立 AI 服务已停用，避免重复回复；网页管理器沿用原登录、文件传输助手/“机器人测试群”白名单和机器人处理进度。其密码在本机私有文件 `secrets/web-local-admin-token.txt` 中，也可从 WSL 读取：

```powershell
wsl -d Ubuntu-New -- cat /home/fly/.local/share/wechat-bot-native-research-20261024/admin/admin-token
wsl -d Ubuntu-New -- systemctl --user status wechat-bot-web
```

API 6174 和手动发送 API 8787 保留原监听；网页内的启停只控制网页管理的自动回复。旧 `wechat-bot-ai` 已禁用，不要与网页管理器同时启动。Windows 本机的 WSL 服务不等于开机启动 WSL，关闭发行版仍会停止运行。

当前 Docker Desktop 采用 system proxy 并透明处理 80/443，测试中它的容器能访问网页和模型 API，却无法完成原始 MMTLS 登录握手；同一程序在 WSL 及 Ubuntu Docker 原生引擎中能取得真实二维码。本次没有修改全局 Docker Desktop 代理设置。Windows 本机请使用上述 WSL 网页；交付镜像以 Linux 原生 Docker Engine 为目标，云服务器仍需实际扫码和收发验收。

## 从源码重建

先按根目录 README 从固定提交构建 `wechat-linux-native:research-7095ef0`，然后在本项目根目录执行：

```bash
docker build -f Dockerfile.web -t wechat-bot-web:0.3.0 .
```

网页静态资源直接编译进 Go 程序，不访问外部 CDN，不需要前端安装步骤。协议来源和许可边界仍以根目录 README 的来源说明为准。
