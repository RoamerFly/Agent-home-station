# 联网检索迁移与兼容边界

## 当前路由

`web_search` 与 `web_fetch` 是稳定工具名称。账号安装并启用 `web-search`
工具时，主体把它们转发至工具的 `web.search` 与 `web.read` 能力，并继续由
宿主执行网络权限、来源校验、任务和活动记录。

未安装该工具的账号继续走原有内置路径。原 `mcp-config.json` 的 `enabled`、
`web_search_enabled` 和 `web_fetch_enabled` 仍是授权开关；本次不读取后写回、
不修改生产账号配置，也不改变已保存的任务。

| 既有账号状态 | `web_search` 行为 | 迁移后的行为 |
| --- | --- | --- |
| 未配置官方 DeepSeek 或搜索失败 | 原有 Bing RSS / 360 公共检索回退 | `web-search` 工具的已配置后端 |
| 官方 DeepSeek、API Key 和检索开关均有效 | 保持原来的 DeepSeek `/v1/tools/search` 付费搜索，再按原逻辑回退公共检索 | 管理员明确导入后端配置、授权 network 并启用工具 |
| 检索开关关闭 | 不发现或调用联网搜索 | 不发现或调用工具搜索 |

因此，这不是“将旧账号静默切成公共搜索”的变更。存在官方配置的账号继续选择
原服务商；没有配置、不可用、无相关结果或超过旧协议查询限制时，仍按旧行为转到
公共搜索。不会自动换到其他付费服务商。

## 本轮兼容包

旧的 DeepSeek 搜索 HTTP 协议已从 `internal/admin` 迁至
`internal/plugincompat/websearch`。管理层只负责已有账号选择、工具活动统计和
公共回退；兼容包只接收 Base URL、API Key 和查询，向官方固定地址发送查询。
它不读取聊天上下文、记忆、素材或微信信息，也不管理账号或投递。

兼容包只支持原来受支持的官方 `https://api.deepseek.com[/v1]` 配置与有效 Key；
不会把兼容服务商、代理地址或聊天模型名称转换成另一个搜索提供商。聊天模型、
思考强度和输出 Token 等参数本来不属于该搜索协议，现在也不会被误传给搜索端点。

原有费用与活动语义保持不变：旧搜索端点没有返回 Token 用量时不会伪造模型费用；
一次兼容请求仍计入原来的 `web_activity.tool_requests`，HTTP 状态和安全错误码继续
进入原诊断字段。现有聊天模型的 Token 账本不把搜索误记成聊天 Token 消耗。

该兼容包是过渡边界，**不是已安装工具，也不代表轻主体完成**。完整迁移仍需在
`web-search` 工具中声明可配置的官方后端、管理员确认密钥映射、权限和费用展示，
并在隔离副本验证后才可修改账号路由。迁移期间不得复制或导出密钥；工具未配置时
不能借此假装具备相同服务商能力。

## 管理员迁移步骤

1. 在隔离副本导入 `plugins/web-search`，核对包摘要和 `network` 权限。
2. 在工具设置中显式选择后端并填写该后端所需配置；不要从旧配置自动复制密钥。
3. 启用工具并用合成请求验证结果、失败诊断和禁用后的拒绝行为。
4. 核对旧账号的 `web_activity`、费用展示和未完成任务。结果未知的任务保持待核对，
   不自动重提。
5. 管理员确认后再切换该账号。保留旧兼容配置读取直到全部账号完成映射。

## 离线验证

```powershell
go test ./internal/plugincompat/websearch ./internal/admin ./internal/mcptools ./internal/deepseek -count=1 -timeout 120s
python -B plugins/web-search/tests/test_runtime.py
```

这些测试使用本机 HTTP 替身或合成输入；没有查询真实搜索站点、消耗付费额度或发送
微信消息。
