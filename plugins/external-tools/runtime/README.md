# 运行时

`main.py` 是标准库 MCP stdio 入口。它只提供 `external.http_request`，每次调用接收宿主绑定的无凭据 `protocol`，仅允许同步 `phase:"call"`；再以 `wechat/external_request` 发出工具名和参数，不建立网络连接。响应可以解析安全 JSON 文本、受控 JSON 媒体 URL 或声明媒体类型的二进制。

禁止指向任意宿主命令，禁止使用旧 MCP 的无隔离启动路径冒充新宿主隔离。
