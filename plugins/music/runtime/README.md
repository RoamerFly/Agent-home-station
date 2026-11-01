# 运行时

`main.py` 是唯一受控 stdio 入口，运行时只接受严格 JSON-RPC 帧。它只能回调宿主的
`wechat/model_request`，固定路由为 `generate`；不导入网络、文件、凭据、GPU 或媒体库。
禁止指向任意宿主命令，禁止使用旧 MCP 的无隔离启动路径冒充新宿主隔离。
