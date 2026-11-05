# 手机任务单一仓储与共享 ADB 生命周期

日期：2026-11-01。环境：Windows amd64、Go 1.25.3、PowerShell。仅使用安全合成状态、假模型、假设备和隔离进程；未修改生产账号、原生登录或运行中的 EXE。

## 实际变化

生产手机服务仍惰性加载，但设置与长期设备任务现在经 `mobile.Repository` 存入账号的 `plugin-host/plugins.sqlite`。通用宿主表 `host_states/host_state_records` 不属于公开插件配置，不挂载到插件 DataDir，也不通过 HTTP/MCP 导出。单次插件调用 `Job` 保持原来的作用，不能用它的完成状态替代整个设备任务或微信投递状态。

首次发现旧 `mobile/settings.json`、`tasks.json` 时，只读校验并计算原始文件的存在性与内容哈希；拒绝链接、非常规文件、超过 4 MiB 的文件、身份不一致、重复请求哈希和其他账号的来源。读取后再次检查文件元数据及哈希。所有设置、任务和导入清单在一个 SQLite 事务内激活；错误不会留下半个活跃仓储，不覆盖原文，也不会自动猜测缺失的请求哈希。

保留原 Task ID、RequestHash、Source、事件、素材引用与观察编号。原 `queued/running` 变为 `needs_review`，不自动重放未知操作。首次空安装直到明确保存才初始化仓储；日常只更新涉及的记录，设置变更与待取消任务原子提交。读取使用同一 WAL 快照，写入以命名空间版本 CAS 防止多个实例覆盖。磁盘写入失败与版本冲突分别保留实际错误，失败后控制器恢复原内存状态。停止操作即使无法持久化，也会立即取消在途上下文并阻断新任务及排队调度；成功重试保存取消状态后才解除阻断。

激活后不再读取或写入旧 JSON，人工修改甚至破坏旧 JSON 也不能改变新仓储。旧 JSON 被移走后，惰性服务仍能发现 SQLite 状态。活跃库损坏、账号不匹配或修订冲突时失败关闭，不回退旧 JSON。通用仓储每条记录 1 MiB、命名空间 8 MiB 是明确的当前存储边界，超出返回错误，不截断。尚未进行大规模性能基准，不能把记录级 SQL 写入等同于所有 CPU 开销均已消除。

截图、录屏和原生操作账本继续使用原宿主私有 `mobile` 目录；不复制模型或媒体。外部旧账本路径仍需要显式映射，不能创建空账本来假装迁移成功。本轮迁出的是设置/任务的持久化权威，不代表队列调度、录屏分析、旧路由和页面已全部从主体删除。

## 删除的执行旁路

`internal/mobile` 不再包含默认 `bridgeFactory/hubBridge`、`mcptools.New` 或直接 stdio 执行。`BrokerFactory` 必须由宿主注入：缺失时不新建任务，错误或空返回不尝试旧命令。`Settings.Bridge` 暂留为旧 JSON 与受控配置迁移的数据类型，不再是直接执行入口。旧 JSON 写入模式仅供现有兼容夹具，生产服务明确使用 Repository。

## ADB 生命周期

受控客户端固定连接 `127.0.0.1:5037`。服务端由宿主以前台 `adb -L tcp:localhost:5037 server nodaemon` 托管，独立于单次 MCP Job；客户端使用 `-H 127.0.0.1 -P 5037`。两者用途不同：AOSP 当前监听实现接受 `localhost`，而数值 loopback 的客户端连接失败不会按默认本地端点自动启动 daemon。[AOSP socket 实现](https://android.googlesource.com/platform/packages/modules/adb/+/f4ba8d73079b99532069dbe888a58167b8723d6c/socket_spec.cpp)、[参数实现](https://android.googlesource.com/platform/packages/modules/adb/+/refs/heads/main/client/commandline.cpp)。

不能只靠 `-H` 宣称无重启风险：AOSP 在客户端与服务端版本不匹配时仍可能终止并重启服务。因此宿主与桥接在执行前核对批准的客户端及服务端协议，遇到不兼容或不可信监听直接失败；这不等于提供了完整的 ADB 协议防火墙。预检与客户端独立连接之间仍有本机端口被外部进程替换的竞争边界，不能宣称对恶意本机进程绝对隔离。[AOSP 版本检查与重启分支](https://android.googlesource.com/platform/packages/modules/adb/+/refs/heads/android15-qpr2-s8-release/client/adb_client.cpp#258)。

宿主只终止自己持有进程句柄的前台服务，已有外部服务仅复用；不会发送 `kill-server` 管理命令。共享引用按端点计数，不按账号或 adb.exe 路径分裂；取消首个调用上下文不会杀死其他引用仍在使用的服务。服务环境只保留必要 OS/用户目录变量，不继承模型密钥、PATH 或端点覆盖变量。进程恰好执行一次 Wait 回收，最后一个引用释放时才关闭自有服务。

启动等待失败与正常关闭失败都保留同一进程及端点的 `closing` 占位，阻止将未清理服务重新当成外部服务。Broker 与 ADB 的 quarantine 使用强引用，关闭重试只清理，不启动服务或重放设备操作。Manager 关闭时分别重试两类未确认清理。

## 迁移与恢复边界

本轮只有合成目录预演。未执行真实账号切换、未制作新备份、未触碰待删除目录。正式更新时先排空设备任务并确认原生 broker 退出，再由单一服务实例读取一致旧状态。导入失败保留来源并暂停设备能力，聊天可继续；不能用空默认配置覆盖故障。

首次激活后旧 JSON 仅为原始资料。接受新写入后不能直接运行旧 JSON 版本回切，否则会丢失新任务进度；应暂停处理并保留新库，由兼容版本修复。媒体与账本的存储位置本轮未变，不需要复制或重新生成现有成品。

## 验证

当前运行记录在本机 `temp/plugin-state-server-validation/`；运行产物不提交。

| 检查 | 证据及边界 |
| --- | --- |
| Go 完整回归 | 最终源码 `go test ./... -count=1 -timeout 600s` 通过，admin 256.275 秒，全部最后协议、停止及读取一致性补丁均包含在该次运行中 |
| 静态检查 | 完整 `go vet ./...`、30 个管理页 JS 的 `node --check` 通过 |
| 并发及构建 | 最终 `go test -race` 的 adbserver/devicebroker/mobile/pluginhost 四包通过；`CGO_ENABLED=0` 的 Windows/Linux amd64 全部 7 个 cmd 构建通过 |
| 插件与目录 | mobile-control 0.3.1 的 15 个包文件摘要通过；最终目录 9 包、114 个索引文件成功 staging |
| Python | 15 项 bridge 回归、13 项规划/门面回归通过；默认 socket probe 本身覆盖真实 `OKAY` 状态帧、分段长度/载荷及失败，不只测试注入回调 |
| 实际隔离 | 官方包经真实 Windows LPAC/Controller/stdin-stdout 连续两轮通过规划与设备门面契约，模型和原生 broker 均为假对象，无付费调用和真实设备动作 |
| 桌面 | 隔离构建通过 55 项控制器断言、86 项生命周期断言、8 轮 WinForms/WebView 退出及 12 个并发单例探针；使用私有桌面，不显示测试窗口到用户任务栏 |
| 数据 | 合成导入身份保留、重复请求哈希拒绝、跨账号拒绝、原 JSON 后续损坏不回退、CAS/写失败回滚、无关记录禁止写触发器、双 Store 快照一致性均有回归 |
| 仓库 | 暂存索引 1,670 个文件，对照 9 项本机私密值，发现项为 0；暂存差异检查通过，无运行产物、真实资料或密钥提交 |

ADB 服务端仅使用本地合成协议监听器与子进程验证，没有运行真实 platform-tools。未调用付费模型、未发送微信消息、未测手机播放。Linux 为本机构建交叉目标，不宣称已在 Linux 主机执行设备测试。桌面测试前一次 staging 在包仍被编辑时因摘要不一致拒绝，包冻结后使用新目录成功；该失败未覆盖或安装生产文件。

整体 `audit_plugin_boundaries.py --require-light` 仍不能放行：语音、音乐、手机具体调度、项目执行等可选实现仍有主体依赖。五类包提供受限就绪能力，九类服务的完整迁出仍未完成。
