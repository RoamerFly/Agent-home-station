# 运行时

`main.py`、`planner.py` 与 `operations.py` 是当前 MCP 运行时：planner 通过宿主绑定的 `wechat/model_request` 规划下一步；五项设备操作只通过 `wechat/device_request` 交给受控宿主代理。运行时不取得本机私有路径、原始 PNG、账号凭据或设备连接资料。逻辑账号/会话等审计身份仍按宿主协议通过调用元数据绑定。

`bridge.py` 是保留的 Android ADB 实现：只接受已登记设备和有限动作，保留观察快照核验、请求键去重、未知回执不重放以及受限录屏素材读取。它不提供任意命令、任意路径、设备安装、登录、代理或网络回退，也不由 `main.py` 启动。

bridge 的 ADB 客户端固定连接宿主拥有的 `127.0.0.1:5037`，不接受配置或环境变量替换端点。客户端的 `-H 127.0.0.1 -P 5037` 是连接用途；宿主 server 则以 `-L tcp:localhost:5037 server nodaemon` 监听，两者不是同一 CLI 语义。每次启动 ADB 客户端前，bridge 先以只读 `host:version` 探测该端点，要求响应负载为 `0029`（十六进制 `0x29`，即协议版本 41）；端点缺失、坏握手或版本不符时不启动客户端。`-H 127.0.0.1` 可阻止缺 listener 时的自动启动，但不能保证 ADB 客户端在探测之后不会遇到同机端口替换或版本处理；bridge 不直接执行 `start-server` 或 `kill-server`，也没有协议代理来消除这段竞态。参见 [AOSP adb_client.cpp](https://android.googlesource.com/platform/packages/modules/adb/+/refs/heads/android15-qpr2-s8-release/client/adb_client.cpp)。共享 server 生命周期和已批准的本机 ADB 二进制版本由宿主负责。

旧 `scripts/mobile_bridge.py` 只保留固定相对路径兼容入口，加载本目录的 `bridge.py`；它不是第二份实现，也不接受外部模块或运行时路径。独立插件入口声明 `model.invoke` 与宿主受控 `device` 代理权限，但不直接拥有网络、ADB 或私有文件资源；包内 bridge 源码的存在不会授予这些能力。

设备观察、操作和录屏的受控代理已接通，但队列、录屏分析调度、专用 UI 与旧配置的整体迁移仍未完成；不能把当前合成验收视为真实设备或全量插件化验收。
