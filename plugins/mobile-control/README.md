# 手机控制

当前包提供 `device.plan` 和五项原生设备能力。`device.plan` 依据宿主绑定的观察、任务目标和允许动作，通过 `wechat/model_request` 请求宿主当前聊天模型规划下一步；返回建议必须在实际提交前重新校验。工具不持有模型凭据或独立的模型设置。

`device.list`、`device.observe`、`device.act`、`device.record`、`device.read_artifact` 在同一个隔离 `main.py` 进程内运行包内 `native.py` / `bridge.py` 高层逻辑，包括 UI 解析、观察一致性和动作语法。它们通过来源绑定的 `wechat/device_io` 请求固定宿主 IO 步骤；不再将整个请求交给第二个 native bridge 进程。工具仅取得当前已授权设备的别名、过滤后的允许动作及包名，不取得真实设备序列号、ADB 路径、主机私有素材路径或原始二进制内容。

宿主保留设备授权、来源版本、原始请求参数、批准的 ADB 二进制、共享 server、物理设备锁和原有 `device-ledger.sqlite`。每个 IO 步骤只允许原请求对应的固定命令；不能提交 shell、argv、其他 serial 或替换 text/URL。未知动作先写原账本，不能因取消、进程退出或迁移自动重放。清理未确认时保留设备锁及强引用，直到确认清理。

截图和录屏以带 SHA-256 与实际字节数的不透明引用经过工具；原始内容始终在宿主私有文件中。素材读取支持明确 offset / bytes 分页，按实际 EOF 返回，不再使用旧 2 MiB offset 或 8 MiB 传输截断；宿主单项二进制资源上限为 128 MiB，超限明确报错。当前录屏仍为原协议规定的 2–12 秒、480×800、256000 bit/s、无声音片段。动作提交不代表目标完成，必须重新观察。

宿主共享 ADB server 固定为 `127.0.0.1:5037`，客户端显式使用该端点，每次固定客户端调用前重新核验宿主 server 绑定。工具不拥有网络、ADB 文件挂载、注册表或子进程权限。只读 readiness 校验配置、包、路径和批准资源，不启动 ADB；实际调用才申请共享 server。

设置与任务状态使用宿主私有 SQLite `plugin-host/plugins.sqlite` 的 `host_states` / `host_state_records`，首次迁移只读核验旧 JSON，保留原任务 ID、来源与哈希，中断任务转为 `needs_review`。原截图、录屏与操作账本继续留在原 host-private mobile 路径。

管理页由宿主渲染声明式设置、设备任务、任务素材和状态区域，固定操作为 `settings.save`、`devices.list`、`tasks.create`、`tasks.cancel`、`tasks.stop`。连接档案新增或替换、具体队列调度和录屏分析仍由宿主负责，全部手机流程尚未整体迁出。

验证范围包括包内单元测试、宿主固定 IO 与原账本回归，以及 Windows LPAC 下真实隔离包协议配合合成硬件结果的观察、动作、录屏和素材分页。没有运行真实 ADB、设备、微信或生产迁移；这些证据不等于真实设备验收。
