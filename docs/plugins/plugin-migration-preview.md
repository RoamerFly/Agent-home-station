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

- `speech`：映射旧云端 TTS 的 endpoint、model、默认声线、表达要求和 key。若宿主只读核验到
  既有受管 `gpt-sovits` manifest、解释器和基础模型均在公共工作区内，且官方 speech 包明确声明
  `local_profile_id=gpt-sovits` 与 ready 的命名合成能力，Go 预演会额外写入该普通配置字段。公开计划只显示
  `resource_proof` 的固定来源 `gpt-sovits-manifest`、存在性与不透明版本 SHA-256；不会输出路径、声线 ID、
  私有声线库、权重、参考音频或密钥，也不会复制任何模型。该资源版本进入 `source_hashes` 和
  `apply_source_hash`，因此 manifest 或解释器变化会使旧管理员批准失效。迁入仍保持禁用，不会因为现有默认云端
  声线或某个同名显示名称而推断本地能力，更不会替换成默认声线。无已登记本地资源时保持原云端预演语义。
  Python 离线工具最多发现受管 manifest 标记并写入 `local_profile_id=gpt-sovits`，同时保留
  `registered_named_speech_profile:gpt-sovits:host_verification_required` 阻塞项；它不能替代宿主路径、
  授权和资源校验，也不宣称该本地能力可执行。ASR、音乐、YuE 和训练仍不迁入 speech 配置。
- `music`：云端和本地桥接分别迁移。旧 `endpoint`、`music_model`、`key` 映射为
  `music_endpoint`、`music_model`、`music_key`；歌曲接口使用地域根地址加
  `/api/v1/services/audio/music/generation`，并去掉根地址末尾的斜杠。旧 `yue_endpoint`、`yue_token`
  仅在官方 music 包明确声明普通 `local_endpoint`、host-only `local_token`、`network` 权限及 ready 的
  `music.bridge_request` 能力时，映射为本地桥接配置。它们不会沿用 TTS 或 ASR 接口，也不会因包升级而
  推测为已支持：缺少该精确合约时计划以
  `official_local_music_bridge_not_declared_by_music_plugin` 阻塞。
  本地 endpoint 必须通过宿主与离线预演共同采用的根地址策略：HTTPS 根地址，或 `localhost`、回环/私有 IP 的
  HTTP 根地址；带路径、查询、凭据、公开 HTTP 地址等旧值以 `local_endpoint_invalid` 阻塞，绝不静默替换。
  `music_enabled` 与 `yue_enabled` 共同只表示旧启用意图，所有 `proposed_enabled` 仍为 `false`。因此有效的
  本地 YuE 配置可以在没有云端 key 时预演和迁入；云端字段不会变成本地桥接依赖。已启用的本地桥接仍要求 token，
  缺少时以 `local_token` 阻塞。
  公开计划中的 `music_key` 与 `local_token` 仅含来源、是否存在及 SHA-256；原始值只可进入显式请求的宿主私密交接项
  `music.music_key`、`music.local_token`，不会放入公开 `values` 或插件配置。`speech-config.json` 的完整哈希进入
  每一步 `apply_source_hash`，所以 endpoint、token 或启用意图变更都会令旧管理员批准失效。当前已迁入的仅是
  受管本地桥接和云端原创歌曲请求；命名歌声、RVC profile、训练、模型和生成媒体仍不迁入。提供商受理不代表音频已验证或微信已发送。
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
ASR 与命名 GPT-SoVITS，管理端验证的是既有登记契约和可核验的资源完整性，不代表已实际执行模型或验证识别质量。通过
预演的迁移仍保持禁用，管理员另行启用后，运行时继续检查所选 profile 和登记版本。

Python 独立预演没有 Manager 的账号资源策略，**不会验证本地登记是否可用**。它只把所需 ASR profile ID
带入待应用配置与审计信息，并始终保留 `registered_local_asr_profile:<ID>` 待宿主验证状态；对命名语音，它只在
受管 manifest 标记存在时写入 `gpt-sovits` 并保留 host-verification 阻塞项。即使本地有同名文件，也不会将其标为
已验证。需要在管理端重新预演并核验资源证明后，才能批准本地 ASR 或命名语音迁移。

离线回归：

```powershell
python -B scripts/test_plugin_migration_preview.py
```
