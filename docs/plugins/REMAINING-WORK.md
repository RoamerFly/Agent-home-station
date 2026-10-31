# 插件迁移剩余工作清单

本清单由 `python scripts/audit_plugin_boundaries.py` 的范围约束而来，不是
完成声明。每一项必须先迁移运行时、配置、来源/权限、任务恢复和结果验证，
再删掉主体中的同类实现。测试、账号数据、模型、运行环境及待删除目录不参与扫描。

## 主体保留范围

主体保留微信登录、收发与回执、账号与认证、记忆与上下文、素材身份与投递、
媒体编码/暂存和插件宿主。`发送已有音频` 与 `生成音频` 的边界保持分开。

## 优先迁移队列

| 插件 | 当前主体残留的主要入口 | 完整迁出需要完成的工作 |
| --- | --- | --- |
| media-understanding | `understanding.go`、`video_observation.go`、`video_inspect*.go`、`local_asr.go` | 接通 ASR 宿主展开、费用和来源校验；迁出视频解码/显式读取；完成配置迁移后删除旧视觉/ASR直接调用。 |
| share-reader | `channels_settings.go`、`channels_understanding.go`、`share_*` | 迁移视频号对象桥接和旧配置；由受控素材交接给媒体插件；删除旧公开页/Channels解析分支。 |
| speech | `speech.go`、`voice_synthesis.go`、`voice_agent.go`、`bailian` | 迁出云端与本地声线生成、声线训练和任务恢复；主体只保留素材校验与微信投递。 |
| music | `music_*.go`、`yue.go`、`singing_policy.go` | 迁出 YuE/RVC、训练、GPU服务、恢复和输出校验；插件显式返回成品约束，禁止默认声线替代。 |
| mobile-control | 惰性 `deviceService`、`mobile.go`、设备路由和静态 `plugins/mobile-control/backend` | 桥接源码和 planner 已在插件目录；仍需受控设备代理、独立进程、队列/配置/页面迁移，移除静态实现依赖；主体只保存来源、素材和投递相关结果。 |
| project-development | `agent_workspace.go`、`agent_process.go`、`project_*.go` | 将项目登记、读写、命令执行迁到工作区权限插件；主体提供受限工作区和授权代理。 |
| web-search | `mcp.go`、`service_web_compat.go`、旧 `mcptools` 调用 | 完成真实账号配置迁移，删除兼容搜索/网页读取分支，保留插件失败与撤权语义。 |
| visual-generation | 生成接口和对应 UI/任务路径 | 迁出图像/视频生成适配器、异步任务和结果素材校验；未有实现的能力不可标记可用。 |
| external-tools | MCP、外部 HTTP/API 编辑器和调用路径 | 合并现有配置入口，迁出远程声明与调用，完成凭据、取消、结果验证和迁移。 |

## 每项服务的删除门槛

1. 正式包状态为 `ready`，可发现能力只包含已完整实现的能力。
2. 配置、密钥、旧配置迁移和权限界面有单一权威存储。
3. 受控来源/素材、异步任务、取消、恢复、结果校验和实际费用语义通过合成测试。
4. 已安装账号不可回退旧实现；未安装账号的兼容路径和迁移预演有明确期限。
5. `audit_plugin_boundaries.py` 中该能力的主体实现引用归零，或有记录的核心保留豁免。
6. 删除旧路由、设置页、提示词和无引用脚本后，执行完整回归和 Windows/Linux 构建。

## 验收命令

```powershell
python scripts/audit_plugin_boundaries.py
python scripts/audit_plugin_boundaries.py --require-light
```

第一条输出当前残留文件及能力状态；第二条只应在九类服务均实际迁出后成功。当前失败是预期结果，不能人工忽略。

审计还会列出 `static_plugin_implementation_dependencies`：主体若静态引用 `wechat-bot/plugins/<插件>/...` 的 Go 实现，同样阻止轻主体放行。仅提供声明的 `wechat-bot/plugins` 根 catalog 不属于实现依赖。
