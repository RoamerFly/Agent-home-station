# 迁移

默认云端配置仍从旧 `speech-config.json` 单向导入：Endpoint 与固定 SpeechSynthesizer 路径形成 `tts_endpoint`，Key 进入宿主私密 `tts_key`，TTSModel、Voice、Instruction 分别映射为 `tts_model`、`default_voice`、`default_instruction`。旧配置预演与批准不自动启用工具。

本地命名说话声线须显式选择 `local_profile_id=gpt-sovits`。宿主复用原受管 GPT-SoVITS 项目注册和声线库；不复制、改写或下载用户模型。云端和本地配置相互独立，本地可在无云端密钥时启用。空 local_profile_id 保持本地关闭，不能自动从旧目录推断启用。

完整推理实现从 `scripts/gpt_sovits_tts.py` 移至哈希索引的 `runtime/gpt_sovits.py`。宿主必须直接运行当前已安装且验证过的包脚本，不能继续执行受管项目中旧 entry 副本。源码仓库旧脚本只兼容定位正式包源码，不再复制推理业务。单独复制旧兼容脚本到其他目录不构成可运行的正式包。

不迁移 ASR、音乐、YuE、RVC 或训练。命名资源始终显式绑定，失败不会回退默认声线。保留原用户数据、运行环境、素材和模型；未知任务状态先查询，禁止自动重复提交推理或投递。
