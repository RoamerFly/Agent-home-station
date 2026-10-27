# 大肥鱼 · 微信 AI Agent

带网页管理端的微信 Agent。支持角色聊天、图片/表情包/语音与分享内容理解、联网/MCP、插件、持久任务、媒体发送和本地 YuE 音乐生成。

本机管理端：**http://127.0.0.1:8081/**。界面版本 `0.31.3-context-song-local`。源码仓库：[RoamerFly/wechat-agent](https://github.com/RoamerFly/wechat-agent)。

## Windows 本机启动

双击根目录 **[启动大肥鱼.bat](启动大肥鱼.bat)**。它恢复已有WSL后台，保留微信登录和配置，网页就绪后打开浏览器；后台运行，不留下常驻命令行窗口。失败时显示提示，日志保存在 `temp/launcher/`。

可选命令：

```powershell
.\启动大肥鱼.bat Web      # 只打开已运行的管理页
.\启动大肥鱼.bat Music    # 按需启动本地YuE，不启用开机自启
.\启动大肥鱼.bat Status   # 查看管理页和音乐服务监听状态
```

此入口适配本机已安装的 `Ubuntu-New` / `fly` 实例。新电脑先按[运行与部署说明](deploy/README.md)准备环境；仅克隆源码不能恢复另一台机器的微信登录。

## 目录导航

| 目录/文件 | 用途 |
|---|---|
| `启动大肥鱼.bat` | 日常启动入口 |
| `cmd/`、`internal/` | Go服务入口、后端、内嵌网页和测试 |
| `scripts/` | 稳定路径的运行脚本、媒体桥接和开发检查 |
| `components/` | YuE源码集成和原生客户端等依赖的来源清单 |
| `docs/` | [文档导航](docs/README.md)：使用、Agent、投递、媒体、UI、手机、设计 |
| `deploy/` | 运行部署说明与 `docker/` 中的历史容器草稿 |
| `examples/`、`configs/` | 插件示例与实验配置，不能替代私有运行配置 |
| `design/` | 用户选择的视觉资产、设计与音色素材清单 |
| `research/` | 协议研究、补丁和开发复核脚本，非日常使用入口 |
| `secrets/` | 本机私有密钥/配置，不上传Git |
| `temp/` | 本机日志、下载和生成媒体；并非所有内容都可删除 |
| `dist/` | 本机构建产物，不上传Git |

日常操作看[使用说明](docs/guide/USER-GUIDE.md)，能力与验证范围看[功能清单](docs/guide/FEATURES.md)，接口扩展看[插件文档](docs/guide/PLUGINS.md)。

## 开发与验证

```powershell
go test ./...
go vet ./...
go build ./cmd/...
node --test scripts/test_admin_rounds.cjs scripts/test_flow_model.cjs
python scripts/check_repository.py
```

约定见 [AGENTS.md](AGENTS.md)。API Key、微信会话、真实聊天、模型、运行环境和原始证据不提交。源码检查、合成测试与真实微信回执、手机播放验收分别记录。

原生微信接入复用固定版本的 [thisnick/agent-wechat](https://github.com/thisnick/agent-wechat)，来源及补丁见 [components/native-client](components/native-client/README.md)。当前暂不构建、导出Docker镜像或发布版本。
