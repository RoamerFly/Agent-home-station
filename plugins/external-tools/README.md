# 外部工具连接

范围：已有 HTTP/MCP 配置。

当前状态：`external.http_request` 与 `external.job_request` 均是独立 stdio 的固定能力。前者只执行一个 HTTP `call`；后者只执行一个旧任务 `start`、`poll` 或 `cancel` 阶段，不循环等待。二者接收宿主逐次绑定的无凭据 `protocol` 投影，端点、请求头、模板、密钥、来源和持久票据仍由宿主权威校验。外部 MCP 与旧的“有 Poll 但无 Job”声明仍保持 `migration-pending`。

宿主协议见 ../../docs/plugins/PROTOCOL.md。不得绕过宿主直接发送微信；生成结果与投递回执分别记录。
