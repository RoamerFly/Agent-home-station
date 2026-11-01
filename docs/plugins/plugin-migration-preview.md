# 插件配置迁移预演

`scripts/plugin_migration_preview.py` 是一个**只读**预演器。它读取管理目录中主账号和
`accounts.json` 所列未移除账号的旧 `config.json`、`speech-config.json`、
`mcp-config.json`、`channels-config.json`，生成可供后续事务式迁移器消费的版本化计划。

它不会安装或启用插件，不写入 SQLite 配置，不复制模型、素材、聊天记录或记忆，也不修改
任何旧配置。计划中的 `proposed_enabled` 始终为 `false`；即使旧能力开启，也必须在核验
该计划、插件包摘要和权限后由管理员再次确认。

```powershell
python -B scripts/plugin_migration_preview.py `
  --admin-root D:\AgentData\wechat-agent\admin `
  --primary-account desktop `
  --output D:\AgentMigration\plugin-plan.json
```

未提供 `--output` 时，安全计划写到标准输出。报告只含密钥是否存在、来源字段和 SHA-256，
绝不含密钥正文。`expected_config_revision: 0` 和包 `version` /
`expected_package_digest`（原始 `plugin.json` 字节的 SHA-256）是未来应用器的乐观并发条件；
应用器必须在一个事务中重新准备受信任的包目录并再次验证它们。

如需由受控、隔离的后续应用器导入密钥，才显式加入：

```powershell
python -B scripts/plugin_migration_preview.py `
  --admin-root D:\AgentData\wechat-agent\admin `
  --output D:\AgentMigration\plugin-plan.json `
  --host-secrets-output D:\AgentMigration\host-secrets.json
```

密钥输出文件必须是新文件且位于源管理目录之外；脚本用排他创建并设置为 `0600`。该文件只供
宿主密钥存储导入，**不得提交、发送、复制到插件目录或放进诊断报告**。当前 Windows 版本会
拒绝 `--host-secrets-output`：Windows 的 `chmod(0600)` 不能证明 owner-only DACL，工具宁可
不导出密钥，也不会把继承 ACL 的文件宣称为私有。Windows 上仍可安全生成不含密钥的公开计划；
待有可验证的私有 ACL 导入器后再进行密钥迁移。

## 映射边界

每个账号按 `media-understanding`、`speech`、`music`、`web-search`、`share-reader` 顺序生成五个步骤。
Python 离线报告和管理端 Go 预演均包含独立的 `music` 步骤；所有步骤默认保持禁用。

- `speech`：仅映射旧云端 TTS 的 endpoint、model、默认声线、表达要求和 key。ASR、音乐、YuE、
  本地声线都明确不迁入。
- `music`：映射 `speech-config.json` 的 `endpoint`、`music_model`、`key` 为
  `music_endpoint`、`music_model`、`music_key`。歌曲接口使用地域根地址加
  `/api/v1/services/audio/music/generation`，并去掉根地址末尾的斜杠；不会沿用 TTS 或 ASR 接口。
  旧 `music_enabled` 只用于标记启用资格，`proposed_enabled` 仍为 `false`。旧音乐已开启而缺少 key
  时，该步骤标为 `blocked`。公开计划中的 `music_key` 仅含来源、是否存在及 SHA-256；原始密钥只可
  进入显式请求的宿主私密交接项 `music.music_key`，不会放入公开 `values` 或插件配置。
  当前包仅就绪云端 `music.generate`，权限为 `model.invoke`；命名歌声、RVC profile、YuE、模型和
  生成媒体均不迁入。提供商受理不代表音频已验证或微信已发送。
- `media-understanding`：映射旧聊天模型为视觉路由，并按旧 ASR 后端生成远程 ASR 或本地 profile
  需求。已启用的 `local` 后端将旧 `asr_local_model` 写入插件普通配置
  `values.local_asr_profile_id`；旧值为空时，计划使用 `sensevoice-small-int8`，同时保留审计字段
  `profile_id`。管理端 Go 预演只读核验官方插件包、当前账号的公开资源登记及其受约束路径、SDK/VAD
  校验记录；成功后把登记的 `ResourceVersion` 放进 `source_hashes.local_asr_profile_resource_version`，
  并纳入 `apply_source_hash`。缺失或无效时保留 `registered_local_asr_profile:<ID>` 前置条件并阻塞应用。
  应用器会重新计算该资源证明；登记版本更新或已绑定资源校验失败都会使旧批准失效。预演不打开插件
  Store、不启用插件、不启动解释器，也不给旧配置回写默认值。
  远程后端不写入 `local_asr_profile_id`。`local_then_bailian` 仍标为
  `asr_backend:legacy_local_then_bailian_requires_explicit_choice`，不会静默改成本地后端或自动填入 profile。
  旧 `bailian.Client.ReviewAudio` 不再保留为主体设置：只要旧 `speech-config.json:key` 存在，预演会把同一地域根映射为
  `audio_review_endpoint=<endpoint>/compatible-mode/v1/chat/completions`、固定
  `audio_review_model=qwen3.8-omni-flash`，并以仅宿主可见的 `audio_review_key` 导入。公开计划只显示该密钥来自
  `speech-config.json:key`、是否存在及其 SHA-256；它不会输出密钥正文。此映射供新的 `media.listen` 音频内容观察使用，和视觉、ASR 独立，且不会仅因旧 key 存在而改变媒体插件原有的启用资格。旧 key 为空时，预演省略这三个听感字段，不阻断已有视觉或 ASR 迁移。
- `web-search`：保留 MCP 开关。当前插件没有官方 DeepSeek 付费检索配置时，带官方 DeepSeek key
  的旧账号会被标为 `blocked`，不会被静默替换为公共搜索。
- `share-reader`：映射视频号 cookie 与助手 cookie 的存在性和私密导入项。仅当已审核的插件
  schema 明确声明普通 `object_bridge_url` 字段时，才会迁入旧的本机环回 HTTP 地址；公网、带路径、
  带查询或其他非法地址仍会阻塞。旧桥接与元宝 cookie 同时存在时，旧版可在桥接失败后回退 cookie，
  新插件链路不会擅自改变这一语义，计划会标为
  `explicit_bridge_fallback_policy_needed`，等待管理员选择。既有 `channels-bindings.json` 仍由宿主
  只读、按账号/会话/消息（或受限对象别名）查询；本轮不复制它、不将其交给插件，也不创建新的绑定。

`required_missing`、`unmapped_legacy_fields` 和插件未 ready 状态必须在真正迁移前解决。对本地
ASR，管理端验证的是既有登记契约和可核验的资源完整性，不代表已实际执行模型或验证识别质量。通过
预演的迁移仍保持禁用，管理员另行启用后，运行时继续检查所选 profile 和登记版本。

Python 独立预演没有 Manager 的账号资源策略，**不会验证本地登记是否可用**。它只把所需 profile ID
带入待应用配置与审计信息，并始终保留 `registered_local_asr_profile:<ID>` 待宿主验证状态；即使本地有
同名文件，也不会将其标为已验证。需要在管理端重新预演并核验资源证明后，才能批准本地 ASR 迁移。

离线回归：

```powershell
python -B scripts/test_plugin_migration_preview.py
```
