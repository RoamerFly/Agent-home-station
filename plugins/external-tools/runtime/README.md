# 运行时

`main.py` 是标准库 MCP stdio 入口。它只解析宿主在 initialize metadata 中提供的、无凭据的声明目录；HTTP 工具仅发出 `wechat/http_route` 的固定 route 意图，不建立网络连接。MCP 声明当前只解析保留，未暴露调用工具，直到宿主提供按账号/会话隔离的连接代理。

禁止指向任意宿主命令，禁止使用旧 MCP 的无隔离启动路径冒充新宿主隔离。
