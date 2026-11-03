# 运行时

`main.py` 使用标准库实现 MCP stdio。默认能力由 `cloud_tts.py` 构造云端请求；命名能力由 `named_voice.py` 严格检查参数和宿主返回值，只调用一次 `wechat/local_speech`。

命名参数必须恰含：

```json
{"voice":{"id":"registered-voice","version":"<64 lowercase hex resource binding hash>"},"text":"正文","device":"cuda","version":"v2","parameters":{"sample_steps":64},"timeout_seconds":600}
```

`voice.version` 是宿主生成的资源绑定摘要；顶层 version 是模型版本。timeout_seconds 为正有限数，原值传递；宿主提供默认值。模型 device/version 和调参显式传递，不静默降级 CPU、不限缩真实推理参数范围。正文无云端 2000 字限制，仍受协议帧和宿主实际资源限制。

宿主返回必须恰含：

```json
{"status":"synthesized","audio":{"ref":"audio_0","sha256":"<64 lowercase hex>","bytes":1000,"sample_rate":24000,"seconds":0.1},"voice":{"id":"registered-voice","version":"<resource binding hash>"},"device":"cuda","version":"v2"}
```

桥不接受额外字段、路径、凭据、其他声线身份或模型版本。音频引用只表示宿主私有验证结果；核心负责素材登记和微信投递。

`gpt_sovits.py` 是完整文件推理实现，只供宿主隔离工作者执行，stdio 不执行它。CLI 保持原有 `--project --models [--packages] --gpt --sovits --reference (--prompt-text|--prompt-text-file) --text --output --device --version --[no-]half --text-lang --prompt-lang [--parameters JSONFILE] [--overwrite]`。运行时、上游 SDK、基础模型和已选声线文件由宿主只读绑定，输出及缓存写入私有作业目录。

`enroll.py --manifest <绝对manifest.json>` 是仅供宿主调用的私人声线作业协议：`prepare` 只核验受控 WAV 样本的哈希、时长及本地静音估计；`preview` 用参考音频合成未见文本，固定产出 `reference.wav` 与 `preview.wav`，不会登记已训练声线；`train` 必须有明确确认、固定 GPT/SoVITS/鉴别器底模哈希、有效人声时长和经版本化分析器确认的单说话人样本。训练可在同一作业目录恢复，`cancel` 会中断当前子进程并保留检查点。只有 `gpt.ckpt`、`sovits.pth` 和训练后 `preview.wav` 都被宿主再次核验后才可发布版本。`base_models/enrollment-v2.json` 是官方 v2 基础权重的固定 SHA-256 锁；`provision_enrollment_v2.py` 只安装与该锁匹配的公开原件。

`voice_enroll` 是宿主面向已验证主人的专用适配：宿主先绑定微信身份、原始样本、同意/单说话人确认、私有作业目录与资源清单，才会运行本脚本。它不是 stdio 的通用 `speech.train` 工具；后者保持不可发现，避免普通插件调用绕过身份、素材范围、GPU 授权或声线注册校验。

Windows 宿主启动该 runner 及其训练子进程时使用受控后台模式：解释器带 `-X utf8`，无交互 stdin，训练子进程使用 `CREATE_NO_WINDOW`。这样既避免本地路径中的中文被系统 GBK 误解，也不会弹出黑色控制台窗口。

工作者 JSON 回执保留 output/gpt/sovits 路径供宿主核对，并报告 sample_rate、seconds、sha256、device、version、wechat_sends=0；新增 gpt_sha256、sovits_sha256、reference_sha256、prompt_sha256、text_sha256。prompt_sha256 使用实际 UTF-8 参考文本（文件输入去 BOM 并 strip），text_sha256 使用原始正文 UTF-8。模型与参考文件在推理前后检查哈希。回执不包含 effective_parameters，路径只在宿主私有通道中存在。

parameters 支持未知推理调参键及嵌套值，不采用采样参数白名单。正文、参考文本、参考录音、模型、权重、文件路径、输出、设备/版本和流式模式属于宿主绑定字段，禁止调参覆盖；嵌套路径、URL、非有限数也被拒绝。文件合成仅产生完整 WAV。
