# 外部工具连接

范围：已有 HTTP/MCP 配置。

当前状态：`external.http_request` 已是独立 stdio 的固定同步能力。它接收宿主逐次绑定的无凭据 `protocol` 投影，只执行一个 HTTP `call` 阶段，并把结果交回宿主校验。端点、请求头、模板、密钥和异步任务仍留在宿主；其他外部能力继续保持 `migration-pending`。

宿主协议见 ../../docs/plugins/PROTOCOL.md。不得绕过宿主直接发送微信；生成结果与投递回执分别记录。
