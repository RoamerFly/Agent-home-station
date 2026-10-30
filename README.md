# 大肥鱼 · 微信 AI Agent

带网页管理端的微信 Agent。支持角色聊天、图片/表情包/语音与分享内容理解、联网/MCP、插件、持久任务、媒体发送和本地 YuE 音乐生成。

本机管理端：**http://127.0.0.1:8081/**。界面版本 `0.35.0-managed-workspaces-local`。源码仓库：[RoamerFly/wechat-agent](https://github.com/RoamerFly/wechat-agent)。

支持[多个微信小号同时在线与分别管理](docs/guide/MULTI-ACCOUNT.md)。在“系统 → 账号管理”新增小号或重新扫码登录，左侧选择当前管理账号；切换页面不会暂停其他账号。

[受管工作区与跨会话附件](docs/agent/managed-workspaces.md)将文件和 Shell 限于账号/会话目录；管理员可临时检索当前账号资料并明确指定媒体目标群。音乐项目已复制校验，GPU 沙箱兼容与真实跨群投递仍按文内验收范围推进。

## Windows 本机启动

推荐使用 **Windows 桌面版 `dist/windows-desktop/WechatAgent.exe`**：打开 EXE 默认最大化并自动启动服务，窗口直接显示管理页；关闭窗口默认弹窗选择取消、收起到托盘或退出，可记住选择。只有选择退出才等待在途任务后停止全部本实例服务。构建、依赖和数据保留说明见 [Windows 桌面程序](desktop/windows/README.md)。

新增 **Windows 原生便携包**：`scripts/Build-Desktop.ps1 -OutputDirectory dist/windows-native -Test -Portable`，整体复制构建目录后双击 EXE 即可启动微信后台和管理页，基础收发无需 WSL。已有 WSL 配置继续保留，不自动迁移账号与资料；可选音乐/模型环境另行部署。首次扫码及真实媒体验收边界见上述桌面文档。

原有 **[启动大肥鱼.vbs](启动大肥鱼.vbs)** 继续作为脚本兼容入口：恢复已有 WSL 后台并打开外部浏览器。

原有BAT入口仍兼容命令行参数；BAT本身由cmd解释，双击可能短暂显示窗口，日常请使用上面的VBS入口。YuE守护任务通过pythonw启动，不分配控制台，也没有开机/登录触发器。

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
| `启动大肥鱼.vbs` / `启动大肥鱼.bat` | 无窗口启动入口 / 命令行兼容入口 |
| `cmd/`、`internal/` | Go服务入口、后端、内嵌网页和测试 |
| `scripts/` | 稳定路径的运行脚本、媒体桥接和开发检查 |
| `components/` | YuE源码集成和原生客户端等依赖的来源清单 |
| `docs/` | [文档导航](docs/README.md)：使用、Agent、投递、媒体、UI、手机、设计 |
| `deploy/` | 运行部署说明与 `docker/` 中的历史容器草稿 |
| `examples/`、`configs/` | 插件示例与实验配置，不能替代私有运行配置 |
| `design/` | 用户选择的视觉资产、设计与音色素材清单 |
| `research/` | 协议研究、补丁和开发复核脚本，非日常使用入口 |
| `secrets/` | 本机私有密钥/配置，不上传Git |
| `public_workspace/` | 受管公共项目、模型、运行时与项目清单，不上传Git |
| `pri_workspace/` | 按账号、会话隔离的记忆、文件和生成物，不上传Git |
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

跨平台拆分、插件环境、新机安装和后续镜像规划：见 [镜像构建方案](镜像构建方案.md)。当前使用本机 WSL 后台，暂停镜像构建和导出。
