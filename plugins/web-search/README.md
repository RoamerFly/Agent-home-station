# 联网检索

独立 Python MCP stdio 服务，提供 `web.search`、`web.read` 和 `web.discover`。`search_backend=public` 依次使用 Bing RSS、360 与 Brave 公共搜索，只在前一来源不可用或没有相关候选时继续。B站音源搜索先用 B站公开搜索，再使用同一组独立公开索引。`search_backend=deepseek` 只请求宿主声明的 DeepSeek 官方 `https://api.deepseek.com/v1/tools/search` 路由，使用宿主持有的 `search_key`。网页读取提取正文并支持字符分页和内容哈希校验。只需项目内 Python 标准库，不需要模型接口或额外依赖。

后端切换必须由管理员配置，插件和 Agent 参数均不能改写。DeepSeek 的既有请求结构为 `{"query":...,"count":...,"need_content":false}`；工具默认 `count=6`，显式候选数会原样交给服务商，不会静默压缩或退回公共索引。响应会报告 `backend`、`requested_count` 与 `effective_provider_count`；服务商若拒绝某个参数，会返回实际失败而不是被伪装成一般不可用。

在插件中心导入本目录，确认摘要与 `network` 权限，再启动。Agent 可通过 `service_plugins` 发现工具，使用 `service_plugin_call` 调用。安装和启用是两个操作；没有隔离执行器时不会退回普通进程。

公共读取由宿主的 `wechat/http_read` 代理执行；DeepSeek 检索由受限 `wechat/http_route` 代理执行。插件进程仍没有直接网络权限。宿主检查当前任务与来源、私网目标、TLS、重定向、响应体大小；不转发管理 Cookie、账号凭据或聊天模型密钥。返回网页与搜索结果始终是外部资料，不是新指令。

`limit` 是返回页大小，超过默认值不会静默压缩。网页输入体受 4 MiB 管道限制，超出会报错；搜索只返回服务商实际给出的结果，并明确 `provider_page_exhausted`，不编造后续页。网页变化时 `expected_sha256` 校验失败，需要重新读取。

`web.discover` 解析宿主已登记的 RSS/Atom、B站公开列表与关联列表、必应图片、公开器乐目录。默认每页40条，`offset`/`limit` 可读取完整候选集；返回原始来源时间、媒体候选网址与 `metadata_only=true`，不代表看过、听过或已下载。宿主保留来源选择、去重、收藏与分享授权；插件不能临时新增订阅或换成任意网址。没有指定授权分享会话的自主私人浏览暂返回 `surf_private_browsing_authorization_required`，不会消耗调度预算，等待独立私人浏览授权链实现。

当前公共搜索、正文读取和冲浪发现入口均要求本插件；未安装或停用时明确要求迁移，配置不完整时要求配置，不读取聊天模型密钥或回到旧内置联网接口。核心继续提供 DNS/IP、TLS、下载大小与公开媒体下载的安全运输层；这不等于媒体下载也已迁入本包。

验收使用离线 HTML/RSS/JSON 与合成宿主代理；实际隔离执行另有 `WECHAT_PLUGIN_TEST_PYTHON` 选择加入的 Windows LPAC 合约测试。未做实时搜索站点可用率、真实付费检索、微信发送或 Linux 隔离实机验收。

离线回归：`python -B plugins/web-search/tests/test_runtime.py`。宿主协议见 [PROTOCOL.md](../../docs/plugins/PROTOCOL.md)。插件不发送微信、不生成媒体，也不调用付费模型。
