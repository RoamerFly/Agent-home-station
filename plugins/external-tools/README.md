# 外部工具连接

范围：已有 HTTP/MCP 配置。

当前状态：独立 stdio 进程可离线解析管理员提供的 HTTP/MCP 声明，并把 HTTP 调用投影为不含 endpoint、请求头或凭据的宿主请求意图。宿主尚未接线真实配置、来源绑定、任务恢复和结果校验；因此能力仍是 `migration-pending`，不得作为可调用能力宣传。

宿主协议见 ../../docs/plugins/PROTOCOL.md。不得绕过宿主直接发送微信；生成结果与投递回执分别记录。
