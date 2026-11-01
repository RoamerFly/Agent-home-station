# 手机控制

当前状态：`ready`。`device.plan` 依据宿主已绑定的单次观察、任务目标和允许动作，调用宿主当前聊天模型规划一个经过本地校验的下一步；`device.list`、`device.observe`、`device.act`、`device.record` 与 `device.read_artifact` 仅经宿主受控设备代理运行。

`device.plan` 不读取设备、原始 PNG、本机私有文件或网络；不启动 ADB，不执行操作，不录屏，不发送内容。宿主调用元数据包含逻辑账号与会话身份，用于审计；不包含微信登录资料或模型凭据。插件没有模型密钥、模型名称、思考强度或用量配置，全部由宿主当前聊天模型、思考设置和用量统计处理。

返回的 `step` 是建议而非执行结果。宿主必须在实际提交前重新校验授权、来源版本、设备状态和动作参数；动作提交也不代表目标完成。模型格式错误最多得到一次修复机会，提供方失败不会由插件重复调用。

`device.list`、`device.observe`、`device.act`、`device.record` 与 `device.read_artifact` 已接入宿主受控 `wechat/device_request` 代理。实际设备授权、broker、队列、动作去重和原始截图/录屏私有保存仍由宿主负责；原始 PNG、录屏片段及其 bytes 不经过插件进程，Agent 不能借此自行升级权限或扫描设备。旧队列、配置和 UI 的完整迁出仍未完成，且本轮没有真实设备验收或旧账本自动迁移声明。

兼容 bridge 只接受宿主派生的 ADB profile：`adb_host=127.0.0.1`、`adb_port=5037`。每次客户端调用前先以只读 `host:version` 校验协议 41（响应为 `0029`），并显式指定该端点、清除可覆盖端点的 ADB 环境变量；端点不可达、握手异常或版本不匹配时不启动客户端。`-H 127.0.0.1` 只解决缺 listener 的自动拉起，不代表 ADB 客户端在 probe 与执行之间绝不会遇到被替换端口或协议处理；bridge 从不直接请求启动或停止 server。

宿主配置与任务状态已迁入其私有 SQLite 命名空间 `plugin-host/plugins.sqlite` 的 `host_states` / `host_state_records`。首次迁移只读核验旧 JSON，保留原任务 ID、来源与哈希；中断的 `queued/running` 转为 `needs_review`，未知操作不自动重放。启用后只写 SQLite 差异记录并使用 CAS，不再双写 JSON。截图、录屏和 bridge ledger 继续留在原有 host-private mobile 路径，不被复制到插件或外部 ledger。

管理页由本包的声明式四个区域组成：执行设置与已登记连接、设备任务、任务素材、状态与说明。数据仅来自宿主的插件详情；设置仍以宿主唯一配置仓库为准，不复制到通用 `config.schema.json`。当前固定受控操作为 `settings.save`、`devices.list`、`tasks.create`、`tasks.cancel` 和 `tasks.stop`，不提供任意 bridge 命令、ADB 参数、脚本、URL 或客户端 JavaScript。新页面只保留既有登记，连接档案新增或替换仍需宿主管理员兼容路径；该登记流程及具体设备调度尚未完整迁出。
