# 旧设置迁移：管理员应用流程

此流程只把可验证的旧设置复制为官方工具的**未启用配置**。它不是导入任意工具、
不会启动工具、不会调用模型，也不会发送微信消息。

## 历史版本的管理页流程

新版工具中心已移除手动迁移入口。以下保留旧部署维护时使用的接口说明。

在“扩展与连接 → 工具 → 服务工具”顶部的“迁移旧设置”打开只读预演。预演按当前
微信账号分组，每个工具单独显示以下绑定信息：

- 当前账号；
- `apply_source_hash`（旧设置的来源摘要）；
- `package.expected_package_digest`（固定官方包的准备摘要）；
- 该工具声明、且管理员选择授予的权限。

页面不会显示旧设置值、密钥或密钥内容。缺少来源摘要、官方包摘要，或存在
`required_missing` 时，该工具仅显示阻断原因，不能确认应用。`unmapped_legacy_fields`
仅说明这些旧设置仍保留在原处；它本身不会阻断已经可安全迁移的部分。旧功能保持原设置，
不会被关闭或替换。

每个可迁移工具都必须重新勾选“已核对”才能提交。变更所选权限、当前账号、登录状态，
或当前工具配置草稿，会使该确认失效。应用成功后工具仍为未启用状态；管理员应在工具
详情检查非私密配置，再单独启动。

## HTTP 契约

`GET /api/plugin-migrations/preview` 返回只读的 `plugin_migration_preview`。前端只使用
当前账号对应的步骤，不因账号名相近而回退到其他账号。

单个步骤要成为可应用候选，至少需要：

```json
{
  "plugin": "speech",
  "status": "ready_for_administrator_review",
  "apply_source_hash": "...",
  "package": {"expected_package_digest": "..."},
  "permissions": ["model.invoke"],
  "required_missing": [],
  "unmapped_legacy_fields": ["asr_*"]
}
```

管理员确认后，前端仅提交下列字段：

```json
{
  "account": "当前账号",
  "plugin": "speech",
  "apply_source_hash": "预演返回值",
  "prepared_package_digest": "预演返回值",
  "permissions": ["管理员保留的权限"],
  "confirm": true
}
```

目标为 `POST /api/plugin-migrations/apply`。该接口在服务端重新读取旧来源、重验官方
目录与包摘要，并以固定的官方目录准备安装；不接收目录、令牌、密钥、配置值或启用标志。
`201` 表示配置已保存为未启用工具。`409` 表示来源、包、权限、账号目标或服务状态已
变化，必须重新打开预演；客户端不得重试旧确认。

## 验证边界

旧迁移的后端契约由 Go 测试覆盖。新版新建向导的浏览器回归见 `scripts/test_plugin_creation_ui.cjs`。
