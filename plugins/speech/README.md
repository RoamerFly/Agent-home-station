# 语音生成

`speech` 0.2.0 提供两项独立能力：

- `speech.synthesize`：默认云端说话声线，通过宿主固定的 `synthesize` HTTP 路由生成音频候选。需要完整的 `tts_endpoint`、宿主私密 `tts_key`、`tts_model` 和 `default_voice`。
- `speech.synthesize_named`：已登记的 GPT-SoVITS 命名说话声线。配置 `local_profile_id=gpt-sovits`，由宿主解析原受管项目及所选模型、参考录音和参考文本，运行包内的完整推理脚本。无需云端密钥。

空配置不会启用任一能力。云端与本地分别由宿主检查就绪状态；启用其中一项不要求另一项已配置。命名声线使用明确的 `voice.id` 和资源摘要 `voice.version`；顶层 `version` 是 GPT-SoVITS 模型版本，两者不能混用。禁止命名声线错误时改成默认云端声线或其他模型。

stdio 进程只拥有 `artifact.read`、`model.invoke` 的宿主服务访问权限，没有 GPU、工作区、任意进程、直接网络、密钥或微信发送权限。实际本地推理由已有授权的宿主 OS 隔离运行器执行；Windows 沿用管理员已批准的 GPU 运行策略。包不携带第三方运行时、SDK、模型或私人声线文件。

声线查询、克隆和训练能力仍为不可发现、不可调用的迁移占位项。本次不迁移训练、RVC 唱歌声线或自动登记声线。

宿主核验来源、当前权限、任务取消、包与配置版本、模型资源和真实 WAV 后登记素材，并在投递边界复核。生成回执不等于微信接受或手机播放。离线测试只使用合成依赖与 WAV，不证明真实 GPU 推理或人耳音色一致。
