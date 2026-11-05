# Agent-home-station 使用手册

项目已更名，伙伴角色仍叫大肥鱼。当前手册介绍已实现的微信与工具功能；应用化重构进度见[长期成长计划](https://github.com/RoamerFly/wechat-agent/blob/codex/windows-native-backend/docs/design/大肥鱼长期成长阶梯计划.md)。

第一次使用，先完成一次授权私聊的文字回复，再安装需要的工具。这样遇到问题时，可以判断故障发生在微信连接、模型、加工工具还是投递阶段。

| 我想做什么 | 从哪里开始 | 怎样算成功 |
| --- | --- | --- |
| 在 Windows 开起来，或部署到 Linux | [安装与启动](installation.md) | 管理页打开，微信后台可以连接 |
| 扫码、选择模型、收到第一条回复 | [首次配置](first-chat.md) | 指定会话产生执行记录，回复取得微信回执 |
| 让不同群遵循各自的偏好 | [群聊与记忆](conversations.md) | 新回合采用该群规则，回复依据能追到原消息 |
| 把图片做成 PDF，或用自己的声音说话 | [附件与声线](artifacts.md) | 能核对源附件、实际成品和投递目标 |
| 挑选、安装或编写工具 | [工具指南](plugins.md) | 宿主能发现授权工具，工具返回可核验的结果 |
| 查看失败原因、处理未知回执和安全更新 | [排障与维护](operations.md) | 定位失败阶段，恢复时避免重复发送 |

## 适用版本

本手册以 Windows 原生开发分支的 `89b0bfa` 为功能基准。使用较早版本时，可能看不到文档工坊或私人声线入口。源码、便携程序、已安装工具和本机模型分别更新；查看仓库中的功能说明后，还要在自己的管理页检查安装状态。[对应源码](https://github.com/RoamerFly/wechat-agent/tree/89b0bfaae7cec1b2bac45cce56df1f0f13296bdb)

在线教程适合按顺序阅读。维护时可从 [GitHub Wiki](https://github.com/RoamerFly/wechat-agent/wiki) 按问题查找同一套内容。
