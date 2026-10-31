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
绝不含密钥正文。`expected_config_revision: 0` 和包 `version` / `manifest_sha256` 是未来
应用器的乐观并发条件；应用器必须在一个事务中再次验证它们。

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

- `speech`：仅映射旧云端 TTS 的 endpoint、model、默认声线、表达要求和 key。ASR、音乐、YuE、
  本地声线都明确不迁入。
- `media-understanding`：映射旧聊天模型为视觉路由，并按旧 ASR 后端生成远程 ASR 或本地 profile
  需求。`local_then_bailian` 不会被悄悄降级：计划将其标为需要管理员选择。
- `web-search`：保留 MCP 开关。当前插件没有官方 DeepSeek 付费检索配置时，带官方 DeepSeek key
  的旧账号会被标为 `blocked`，不会被静默替换为公共搜索。
- `share-reader`：映射视频号 cookie 与助手 cookie 的存在性和私密导入项；`object_bridge_url` 尚无
  插件字段，会明确报告为未映射。

`required_missing`、`unmapped_legacy_fields` 和插件未 ready 状态必须在真正迁移前解决。对本地
ASR，预演只记录所需 profile ID，不能假装已经验证模型、解释器或资源注册。

离线回归：

```powershell
python -B scripts/test_plugin_migration_preview.py
```
