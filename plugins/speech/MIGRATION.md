# 迁移

默认云端 TTS 从旧 `speech-config.json` 单向导入：`Endpoint` 与固定
SpeechSynthesizer 路径形成 `tts_endpoint`，`Key` 导入宿主私密的
`tts_key`，`TTSModel`、`Voice`、`Instruction` 分别映射为 `tts_model`、
`default_voice`、`default_instruction`。仅在旧 `TTSEnabled=true`、配置完整
且管理员确认后才可启用。

不导入 ASR、音乐、YuE、本地模型、GPT-SoVITS 权重或 RVC 资料。命名声线与
参考音频仍由主体的声线库和素材系统保持身份；不得将它们改写成默认云端声线。

先在隔离副本预演并核对，成功提交后再切换唯一存储；任务状态未知时先查询，
禁止重复提交。启用前可恢复旧备份，接受新写入后保留新数据由兼容版本修复。
