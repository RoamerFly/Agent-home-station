# 外部工具连接

范围：已有 HTTP/MCP 配置。

当前状态：`external.http_request`、`external.job_request`、`external.poll_request` 与 `external.mcp_request` 是独立 stdio 的固定能力。HTTP 只执行一个 `call`；Job 只执行一个 `start`、`poll` 或 `cancel`；无 Job 的 Poll 只执行一个 `start` 或 `query`，由宿主持久保存首次 JSON 响应并调度后续单步查询；MCP 只执行一个目录页或单次调用。运行时接收宿主逐次绑定的无凭据 `protocol` 投影，端点、请求头、模板、密钥、来源、原始响应和持久状态仍由宿主权威校验；运行时不循环、休眠或直连外部服务。

宿主协议见 ../../docs/plugins/PROTOCOL.md。不得绕过宿主直接发送微信；生成结果与投递回执分别记录。
