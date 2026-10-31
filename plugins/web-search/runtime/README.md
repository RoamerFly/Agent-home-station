# 独立运行时

main.py 使用项目内 Python 标准库运行 MCP stdio。只能由宿主隔离执行器启动；公共读取经当前调用的 `wechat/http_read` 权限代理，DeepSeek 检索经固定 `wechat/http_route` 权限代理，不直接建立网络连接或读取密钥。
