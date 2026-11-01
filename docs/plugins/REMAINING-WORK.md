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
| media-understanding | `understanding.go`、`video_observation.go`、`video_inspect*.go`、`local_asr.go` | ASR、图片/表情、视频视觉、录屏分析及歌曲音频观察均走安装插件；旧视觉回退和 ReviewAudio SDK 已删除。继续收拢剩余配置/视频策略，完成生产账号迁移。保留核心下载、实际区间解码、来源及素材校验。 |
| share-reader | `channels_settings.go`、`channels_understanding.go`、`share_*` | 迁移视频号对象桥接和旧配置；由受控素材交接给媒体插件；删除旧公开页/Channels解析分支。 |
| speech | `speech.go`、`voice_synthesis.go`、`voice_agent.go`、`bailian` | 默认云端与命名 GPT-SoVITS 推理均走安装插件，旧直连/登记 Entry 已退出执行链；所选资源、私有完成结果与投递持续校验。继续迁出声线训练、克隆/设计、余下共享配置与主体内专用适配，完成真实账号迁移。主体最终只保留通用授权/资源/素材及微信投递。 |
| music | `music_*.go`、`yue.go`、`singing_policy.go` | music 0.4.0 已接云端生成、本地桥接及服务控制；八项详细工具说明、schema 和采样目录已移入校验安装包，核心净移出约 4.9KB 源码。进程由通用宿主注册表按插件归属管理，停止插件回收其自有服务。继续迁出训练与歌曲编排、剩余主体专用适配并完成生产迁移。保留成品约束与核心素材校验，禁止默认声线替代。 |
| mobile-control | 惰性 `deviceService`、设备队列与 native ADB 代理 | 规划及五项设备门面已接插件；新 stdio 隔离检查揭示旧 native ADB bridge 尚未登记受控后端，不能继续以宿主权限执行。无登记时在启动 ADB 前拒绝，设备能力不得显示就绪；规划可独立可用。已用登记合成包验证 broker 的真实隔离往返，但实际 SDK/ADB 操作仍须迁到受控代理。随后继续迁出具体队列、连接档案和外部旧账本，并完成生产预演；不把合成协议证明当作真实手机验收。 |
| project-development | `project_plugin.go`、`agent_process.go` 的受控代理 | 0.2.0 已迁入项目登记解释、工具声明及项目业务，12 项兼容工具通过安装插件调用；主体保留工作区授权、真实文件/进程执行及来源结果核验。空配置迁移已可应用且默认停用，受限旧权限须重新确认。仍需生产迁移与 Linux 实际进程验收。 |
| web-search | `mcp.go`、稳定工具别名、`surf*` 宿主授权与历史 | 旧付费兼容和公共搜索/网页读取/内容发现网络解析实现均已删除，来源、分页及候选由 web-search 0.3.0 返回；无分享目标时的一次性只读浏览来源也已接通，空非持久记忆且不授发送/下载权限。继续收拢剩余配置/主体专用适配并完成真实账号迁移；核心保留安全运输、来源授权和历史校验。 |
| visual-generation | 生成接口和对应 UI/任务路径 | 迁出图像/视频生成适配器、异步任务和结果素材校验；未有实现的能力不可标记可用。 |
| external-tools | MCP、外部 HTTP/API 编辑器和旧内联轮询 | 0.4.0 同步 HTTP 与持久 Job 的 start/poll/cancel 已通过安装控制器和单步宿主代理执行，旧任务 HTTP 编排已删除；旧任务证明缺失时保留 ID 待核对。stdio 只允许已登记隔离包，按账号/请求者 scope 分配私有目录，撤权关闭进程。继续迁出无 Job 的 Poll、MCP actor/source 代理、统一设置与配置权威；不能据部分能力 ready 宣称整体已迁完。 |

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
