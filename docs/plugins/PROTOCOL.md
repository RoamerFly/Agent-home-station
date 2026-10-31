# 插件宿主协议 v1

包契约与旧 HTTP 工具声明分开，不改变旧 manifest 的含义。plugin.json 的 host_protocol 必须匹配宿主。包摘要只证明完整性，不证明来源可信。管理员仍需确认来源、安装和权限。

## 调用与交付

本地运行目标为 MCP stdio，远程支持 MCP HTTP 和旧 HTTP API 适配。宿主创建账号、会话、请求者、目的、来源版本和幂等键上下文；模型工具参数不能覆盖。插件不能发送微信或读取其他账号。素材仅通过宿主授予的引用和目录交付。

异步结果区分 pending、running、completed、failed、cancelled、needs_review。重启保留插件版本、配置版本、请求约束、远端 ID 和原幂等键；结果未知先查询，不重提。完成生成不代表投递完成。命名声线必须绑定资源版本，不得默认替换。

## 配置与界面

### 受控网络代理

`network` 权限由宿主代理实现，本地插件不因此取得系统套接字权限。在获准的 `tools/call` 期间，插件可发送 JSON-RPC 请求 `wechat/http_read`，参数为 `{url,max_bytes,timeout_seconds}`，使用独立请求 ID。宿主返回同 ID 的 `{ok,url,content_type,body_base64}` 或 `{ok:false,error}`。每次代理调用前后重新验证当前任务的授权与来源；插件不能传入替代账号、身份或认证头。初始化、健康检查和空闲阶段不能使用代理。

代理响应受 8 MiB 帧约束，当前公共正文读取上限为 4 MiB（含 base64 开销）；超限明确失败，不悄悄截断。URL、DNS/IP、TLS 和重定向校验复用核心公共读取策略；内容提取、相关性和分页属于插件。普通 stdio、任务幂等与取消行为保持不变。

schema 声明由宿主渲染。首版组件为 configuration、tasks、resources、diagnostics，不加载插件 JavaScript。密钥是宿主私有引用，不在包或配置响应中回显。缺少隔离、依赖或批准的能力必须返回实际不可用原因。

### 固定凭据接口

需要凭据的 JSON POST 使用 `wechat/http_route`，请求 `{route,body,max_bytes,timeout_seconds,referer?}`。route 必须匹配安装清单 `http_routes` 中的 ID，并属于当前能力。清单记录固定 HTTPS endpoint、允许调用的 capabilities、credential 配置名及 Cookie/Authorization 头。静态头限于 Accept、User-Agent、Origin、Referer；不得将密钥写入静态头。管理员确认页展示凭据目的接口。

配置中 `x-host-only:true` 必须同时为 `x-secret:true` 的字符串字段，不允许默认密钥。该字段可供宿主代理使用，但从发给子进程的初始化配置中删除。代理检查账号、包摘要、插件版本、启用状态、配置版本和 network 权限；业务宿主必须另行校验 body 中的作品编号、来源、引用链。缺少来源绑定回调时拒绝代理。

请求体上限 1 MiB、响应上限 4 MiB；固定接口只发送一次，不自动重试或跟随重定向。底层使用公共网络 DNS/IP 固定与 TLS 检查。动态 Referer 仅允许清单声明的同源路径前缀，不允许指定其他站点或任意认证头。接口返回是私有中间资料，不应直接成为模型或微信发送内容。

当前固定接口基础设施及视频号合成调用已实现；生产分享来源绑定、对象桥接和媒体交接尚未完成，share-reader 仍不得正式安装。

## 实施状态

当前文件定义迁移契约，不表示所有宿主功能已接入。migration-pending 和 template 包不得暴露为业务工具。只有契约测试、权限隔离和对应后端迁移完成后才能标为 ready。
