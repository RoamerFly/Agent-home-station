# 运行时

`main.py` 是唯一隔离 MCP 入口。`planner.py` 通过宿主 `wechat/model_request` 规划下一步；`operations.py` 只负责闭合请求语法；`native.py` 在当前进程调用 `bridge.py` 的高层观察、UI 解析和动作校验逻辑，不再创建第二个进程或发送 `wechat/device_request` 整单中转。

原生操作首先通过 `wechat/device_io` 取得过滤后的 config 对象和不透明 scope。调用元数据固定账号、会话、请求者、原操作和 source_version；config 仅含当前已授权设备别名及允许包名、动作、浏览器和域名。高层逻辑的缓存仅写隔离进程自己的 cwd 下 `native-ledger/<scope>`，不伪造或打开宿主原路径。

IO 请求只能是固定 `profile`、`state`、`foreground`、`screenshot`、`ui`、`dispatch`、`record`、`artifact_read` 步骤，parameters 必须为空对象。宿主从原请求构造固定 ADB 参数，复核设备/包/动作范围、实际观察和物理锁；自由命令、argv、serial、路径或替代 text/URL 均不属于此协议。响应为 `{operation,source_version,result}`，宿主独立核对实际 IO 证明再生成回执和私有文件。

大截图和录屏只以 `{ref,sha256,bytes}` 交给插件；素材分页还包含 offset、实际总长和 EOF。原始 PNG/MP4、base64 以及宿主文件路径不经过 MCP stdio。单项资源 128 MiB 的真实边界明确报错；录屏时长、尺寸和比特率沿用原协议。

物理锁和原操作账本由宿主独占，提交前持久化 unknown，清理不确定时保留锁，后续请求只读取既有结果而不重放。包内源码仍保留旧 `scripts/mobile_bridge.py` 的固定相对导入兼容面供离线旧测试，但当前安装包入口不会调用它，也不获取其 socket/subprocess 权限。

插件没有网络、ADB 挂载或注册表权限。旧 CLI 所需 socket 延迟导入，隔离原生路径不初始化 Winsock、不调用 socket 或 subprocess。Windows LPAC 加合成硬件协议验收不能代替真实设备验收；队列、连接登记及素材分析调度仍由宿主承担。
