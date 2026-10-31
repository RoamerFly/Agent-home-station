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

模型请求使用独立的 `wechat/model_request`，要求 `model.invoke`，普通 `network` 权限不能替代。路由声明 `permission:"model.invoke"`，其余旧路由默认 `network`。`endpoint` 与 `endpoint_config` 二选一：后者引用管理员填写的必填字符串配置，保存、启动和调用时均验证完整 HTTPS 地址，不接受查询、片段、用户信息或原始编码路径。插件不能在调用参数中指定地址或认证头。动态地址路由不接受固定/动态 Referer 或 Origin。

`credential_prefix:"Bearer "` 可用于 Authorization；密钥字段仍须 `x-secret` 与 `x-host-only`，不会进入进程初始化。修改接口地址/密钥须先停用，并产生新配置版本；旧任务不能继续沿用旧版本。安装确认展示使用哪个配置字段指定目的接口，实际地址在配置页展示。

模型调用请求 `{route,body,max_bytes,timeout_seconds}` 中只能携带受控媒体引用。宿主必须提供具体的素材与请求绑定器，核对模型、参数、账号、来源版本和媒体引用后，才将实际字节替换进提供商请求；没有绑定器时代理拒绝调用。绑定前后重新核对包/配置/权限，回复接受还须经过业务策略的来源与结果校验。当前生产素材绑定器未完成，仅有合成策略，不得因此开启真实账号能力。

模型代理引用请求上限 1 MiB，宿主展开后的 JSON 上限 32 MiB，响应上限 4 MiB；这些是传输内存限制，不是自动图像数量或工具页大小偏好。重复字段、无绑定源、超限和不支持参数明确失败，不截断。耗费请求不自动重试。现有 `wechat/http_route` 的 1 MiB 请求体限制保持不变，不能借模型权限访问普通网络路由。

配置中 `x-host-only:true` 必须同时为 `x-secret:true` 的字符串字段，不允许默认密钥。该字段可供宿主代理使用，但从发给子进程的初始化配置中删除。代理检查账号、包摘要、插件版本、启用状态、配置版本和 network 权限；业务宿主必须另行校验 body 中的作品编号、来源、引用链。缺少来源绑定回调时拒绝代理。

请求体上限 1 MiB、响应上限 4 MiB；固定接口只发送一次，不自动重试或跟随重定向。底层使用公共网络 DNS/IP 固定与 TLS 检查。动态 Referer 仅允许清单声明的同源路径前缀，不允许指定其他站点或任意认证头。接口返回是私有中间资料，不应直接成为模型或微信发送内容。

当前固定接口基础设施及视频号合成调用已实现；生产分享来源绑定、对象桥接和媒体交接尚未完成，share-reader 仍不得正式安装。

## 实施状态

当前文件定义迁移契约，不表示所有宿主功能已接入。migration-pending 和 template 包不得暴露为业务工具。只有契约测试、权限隔离和对应后端迁移完成后才能标为 ready。
