# 工具宿主协议 v1

包契约与旧 HTTP 工具声明分开，不改变旧 manifest 的含义。plugin.json 的 host_protocol 必须匹配宿主。包摘要只证明完整性，不证明来源可信。管理员仍需确认来源、安装和权限。

## 调用与交付

本地可执行包当前只支持 MCP stdio。包清单可校验 `mcp-http` 和 `http-adapter` 声明，但通用执行器尚未实现它们的受控传输、端点批准与凭据代理；它们不能被标记为可调用或作为 MCP stdio 的替代。宿主创建账号、会话、请求者、目的、来源版本和幂等键上下文；模型工具参数不能覆盖。工具不能发送微信或读取其他账号。素材仅通过宿主授予的引用和目录交付。

通用动态工具目录只公开受信所有者在**当前账号、当前会话**可调用的 `mcp-stdio` 能力：能力必须 `ready`、`discoverable:true`、非异步，且 `side_effect` 只能为 `read`、`compute` 或省略（纯计算）。零权限能力可直接进入目录。`network` 能力只有一种额外例外：权限必须精确为 `["network"]`，并且清单中只能有一条与该能力绑定的固定公开 HTTPS 路由；路由不得使用可配置端点、凭据、静态请求头或 Referer。工具只能在本次调用内经 `wechat/http_route` 提交一次该路由，`body` 必须与宿主已授权的 capability 参数完全同构；工具不能选择 URL、请求头、认证信息或替换 body。宿主保留调用方上下文、`max_bytes`、`timeout_seconds` 和取消边界，并在实际提交前原子保留；超时或结果未知也不会用同一 Job 再次提交。

最小 network 声明与调用形状如下（地址仅为示例，必须替换为实际公开固定 HTTPS 接口）：

```json
// capabilities.json
{"id":"public.lookup","implementation":"ready","discoverable":true,"async":false,"side_effect":"read","permissions":["network"],"input_schema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]},"output_schema":{"type":"object"}}
// plugin.json 的 http_routes
{"id":"public-lookup","endpoint":"https://api.example.invalid/lookup","permission":"network","capabilities":["public.lookup"]}
// 工具在本次 tools/call 中请求宿主服务
{"method":"wechat/http_route","params":{"route":"public-lookup","body":{"query":"ocean"},"max_bytes":65536,"timeout_seconds":20}}
```

这不是任意联网、下载、认证接口或重试权限：`mcp-http`、`http-adapter`、带凭据/配置端点/请求头/Referer 的路由，以及 network 以外的 device、workspace、model、媒体和发送能力仍必须由专用宿主适配器实现。异步任务、写入、媒体成品、设备和工作区能力也必须由对应适配器验证来源、取消和结果交付；仅修改 capability 声明不会取得这些权限。

Windows 本地 stdio 包始终经宿主受管的 LPAC broker 运行；Python 运行时、`pythonw.exe` 和 broker 脚本是部署提供的可信运行时，不由工具包、配置或模型参数选择。Linux 本地 stdio 包需要 `bwrap`。缺少这些宿主前提时，工具必须显示为不可执行，而不是退回到非隔离进程。

异步结果区分 pending、running、completed、failed、cancelled、needs_review。重启保留工具版本、配置版本、请求约束、远端 ID 和原幂等键；结果未知先查询，不重提。完成生成不代表投递完成。命名声线必须绑定资源版本，不得默认替换。

## 配置与界面

### 受控网络代理

`network` 权限由宿主代理实现，本地工具不因此取得系统套接字权限。`wechat/http_read` 是已有**专用适配器**的来源绑定读取协议，参数为 `{url,max_bytes,timeout_seconds}`；它不对后装通用 network 包开放。后装通用包只能使用上文的单次、固定 `wechat/http_route`。两类代理都会在调用前后重新验证当前任务的授权与来源；工具不能传入替代账号、身份或认证头。初始化、健康检查和空闲阶段不能使用代理。

代理响应受 8 MiB 帧约束，当前公共正文读取上限为 4 MiB（含 base64 开销）；超限明确失败，不悄悄截断。URL、DNS/IP、TLS 和重定向校验复用核心公共读取策略；内容提取、相关性和分页属于工具。普通 stdio、任务幂等与取消行为保持不变。

schema 声明由宿主渲染。首版组件为 configuration、tasks、resources、diagnostics，不加载工具 JavaScript。密钥是宿主私有引用，不在包或配置响应中回显。缺少隔离、依赖或批准的能力必须返回实际不可用原因。

### 固定凭据接口

需要凭据的 JSON POST 使用 `wechat/http_route`，请求 `{route,body,max_bytes,timeout_seconds,referer?}`。route 必须匹配安装清单 `http_routes` 中的 ID，并属于当前能力。清单记录固定 HTTPS endpoint、允许调用的 capabilities、credential 配置名及 Cookie/Authorization 头。静态头限于 Accept、User-Agent、Origin、Referer；不得将密钥写入静态头。管理员确认页展示凭据目的接口。

模型请求使用独立的 `wechat/model_request`，要求 `model.invoke`，普通 `network` 权限不能替代。路由声明 `permission:"model.invoke"`，其余旧路由默认 `network`。`endpoint` 与 `endpoint_config` 二选一：后者引用管理员填写的必填字符串配置，保存、启动和调用时均验证完整 HTTPS 地址，不接受查询、片段、用户信息或原始编码路径。工具不能在调用参数中指定地址或认证头。动态地址路由不接受固定/动态 Referer 或 Origin。

`credential_prefix:"Bearer "` 可用于 Authorization；密钥字段仍须 `x-secret` 与 `x-host-only`，不会进入进程初始化。修改接口地址/密钥须先停用，并产生新配置版本；旧任务不能继续沿用旧版本。安装确认展示使用哪个配置字段指定目的接口，实际地址在配置页展示。

模型调用请求 `{route,body,max_bytes,timeout_seconds}` 中只能携带受控媒体引用。宿主必须提供具体的素材与请求绑定器，核对模型、参数、账号、来源版本和媒体引用后，才将实际字节替换进提供商请求；没有绑定器时代理拒绝调用。绑定前后重新核对包/配置/权限，回复接受还须经过业务策略的来源与结果校验。当前生产素材绑定器未完成，仅有合成策略，不得因此开启真实账号能力。

模型代理引用请求上限 1 MiB，宿主展开后的 JSON 上限 32 MiB，响应上限 4 MiB；这些是传输内存限制，不是自动图像数量或工具页大小偏好。重复字段、无绑定源、超限和不支持参数明确失败，不截断。耗费请求不自动重试。现有 `wechat/http_route` 的 1 MiB 请求体限制保持不变，不能借模型权限访问普通网络路由。

配置中 `x-host-only:true` 必须同时为 `x-secret:true` 的字符串字段，不允许默认密钥。该字段可供宿主代理使用，但从发给子进程的初始化配置中删除。代理检查账号、包摘要、工具版本、启用状态、配置版本和 network 权限；业务宿主必须另行校验 body 中的作品编号、来源、引用链。缺少来源绑定回调时拒绝代理。

请求体上限 1 MiB、响应上限 4 MiB；固定接口只发送一次，不自动重试或跟随重定向。底层使用公共网络 DNS/IP 固定与 TLS 检查。动态 Referer 仅允许清单声明的同源路径前缀，不允许指定其他站点或任意认证头。接口返回是私有中间资料，不应直接成为模型或微信发送内容。

当前固定接口基础设施及视频号合成调用已实现；生产分享来源绑定、对象桥接和媒体交接尚未完成，share-reader 仍不得正式安装。

## 实施状态

当前文件定义迁移契约，不表示所有宿主功能已接入。migration-pending 和 template 包不得暴露为业务工具。只有契约测试、权限隔离和对应后端迁移完成后才能标为 ready。

### 外部持久任务与本地 MCP 流

`external.job_request` 只执行一次 `start`、`poll` 或 `cancel`，不在进程内轮询或睡眠。宿主持有原始参数及 Ticket，工具通过 `wechat/external_request` 仅回传 phase、工具名和两个绑定摘要；实际 HTTP 使用宿主原始值。每个轮次先持久化调用意图；未知创建不重提，取消先保存本地 claim。返回的编号、状态和输出必须与实际响应对应。有效编号已取得但状态或输出不完整时，只保存该编号继续查询，不把它算作完成。

本地 MCP 连接可用 `{"id":"example","transport":"stdio","plugin_id":"已安装包标识","enabled":true}` 选择已启用的登记包。省略 transport 时，带 plugin_id 的连接默认 stdio；command、args、env 不能与 plugin_id 混用。宿主流复用工具 Executor 的完整校验与强制沙箱，不提供自由 argv、环境或挂载覆盖。旧 raw-command 配置仍可读取，但不能直接启动；没有登记时明确返回迁移需求。

每次请求及缓存发现都会重验包、配置和连接快照；变化时关闭旧会话，不在同一 Hub 自动绑定新版本。私有数据按账号和 MCP scope 分区；管理器关闭、控制器关闭或撤权会回收空闲流。此登记校验不能替代聊天请求者、来源及调用目的授权；完整外部 MCP 代理仍待接入。需要 ADB/设备等宿主能力的后端还必须完成自己的受控操作适配，不能据普通 stdio 往返成功宣称设备已经可用。
