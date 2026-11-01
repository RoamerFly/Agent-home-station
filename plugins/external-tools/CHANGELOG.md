# 变更

0.4.0：新增 ready 的固定 `external.job_request`，以单次 start/poll/cancel 对齐旧外部任务状态；宿主保持原始参数、票据、端点和凭据的权威校验。MCP 与无 Job 的旧轮询仍未迁移。

0.3.0：新增固定 `external.http_request` stdio 协议和受控单步 legacy HTTP bridge；运行时只编排同步 call 的解析，宿主保留端点、凭据和实际网络。其他外部能力继续保持 migration-pending。

0.1.0：建立迁移契约，尚未迁出旧实现。
