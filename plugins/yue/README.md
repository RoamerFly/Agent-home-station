# YuE 作曲工具

`yue.generate` 使用管理员登记的官方 YuE 本机后端生成原创或翻唱音乐，`yue.job` 查询或取消当前任务的作业。它只接收受控参数和宿主授权的素材编号；不能传入本地路径、服务地址、任意命令或模型文件。

## 依赖与安装

源码目录不提交第三方运行时、模型或权重。本机安装包的 `runtime/engine` 包含独立的 ComfyUI 与 YuE2 节点、Python；本包 `models`、`workflows` 和 `runtime/media/ffmpeg.exe` 提供作曲所需的本地资源。可在本包目录运行 `./run-service.ps1 -Help` 检查入口，或运行 `./run-service.ps1` 独立启动。GPU 驱动仍由系统提供。

`runtime/managed-service.json` 是协议声明，不是启动脚本。工具显示“本机后端未就绪”（`yue_backend_not_configured`）时，表示尚未完成宿主登记；安装状态不等于可以启动后端。

## 使用边界

1. `mode` 只能是 `original` 或 `cover`；`style`、`lyrics` 和 `duration_seconds` 是明确的生成请求。
2. `reference_artifact_id` 只能引用宿主为当前任务授权的素材。指定参考模式或翻唱但缺少匹配素材时，宿主会明确拒绝，不会替换成默认素材或默认声线。
3. `min_duration_seconds`、`text_guidance_scale`、随机种子、声部、规划、参考和采样参数由宿主按节点能力核验。越界值明确报错，生成回执会列出实际使用的参数。
4. 成功仅表示音频成品已作为当前任务素材登记；聊天和应用发送仍由主体独立核验与投递。
5. `yue.job` 只能查询或取消当前授权范围的作业。取消后的迟到成品不会恢复旧任务投递。

## 验证边界

包契约测试验证声明、哈希、权限、参数和本地分发边界。实际后端启动、GPU 推理、音乐质量、应用服务器接受和终端播放由宿主集成测试或人工验收分别记录。本工具包不包含任何模型、二进制、模板、账号、凭据或真实素材。
