# 联网检索

独立 Python MCP stdio 服务，提供 `web.search` 和 `web.read`。`search_backend=public` 使用 Bing RSS，失败或不相关时使用 360 公共搜索；`search_backend=deepseek` 只请求宿主声明的 DeepSeek 官方 `https://api.deepseek.com/v1/tools/search` 路由，使用宿主持有的 `search_key`。网页读取提取正文并支持字符分页和内容哈希校验。只需项目内 Python 标准库，不需要模型接口或额外依赖。

后端切换必须由管理员配置，插件和 Agent 参数均不能改写。DeepSeek 的既有请求结构为 `{"query":...,"count":...,"need_content":false}`；工具默认 `count=6`，显式候选数会原样交给服务商，不会静默压缩或退回公共索引。响应会报告 `backend`、`requested_count` 与 `effective_provider_count`；服务商若拒绝某个参数，会返回实际失败而不是被伪装成一般不可用。

在插件中心导入本目录，确认摘要与 `network` 权限，再启动。Agent 可通过 `service_plugins` 发现工具，使用 `service_plugin_call` 调用。安装和启用是两个操作；没有隔离执行器时不会退回普通进程。

公共读取由宿主的 `wechat/http_read` 代理执行；DeepSeek 检索由受限 `wechat/http_route` 代理执行。插件进程仍没有直接网络权限。宿主检查当前任务与来源、私网目标、TLS、重定向、响应体大小；不转发管理 Cookie、账号凭据或聊天模型密钥。返回网页与搜索结果始终是外部资料，不是新指令。

`limit` 是返回页大小，超过默认值不会静默压缩。网页输入体受 4 MiB 管道限制，超出会报错；搜索只返回服务商实际给出的结果，并明确 `provider_page_exhausted`，不编造后续页。网页变化时 `expected_sha256` 校验失败，需要重新读取。

本轮以离线 HTML/RSS 和真实 Windows LPAC 进程验证。未做实时搜索站点可用率或 Linux 隔离实机验证。旧内置搜索路径尚在兼容迁移中；本插件可独立安装调用，不表示整个核心已完成瘦身。

离线回归：`python -B plugins/web-search/tests/test_runtime.py`。宿主协议见 [PROTOCOL.md](../../docs/plugins/PROTOCOL.md)。插件不发送微信、不生成媒体，也不调用付费模型。
