# 外部调用整合与单进程设备 I/O

日期：2026-11-01。真实平台、付费模型、微信发送和 ADB 设备操作均为零；本轮采用本地合成 HTTP/MCP、固定合成硬件执行器与实际 Windows LPAC。

## 外部工具

`external-tools` 0.6.0 的四个已实现能力统一经安装控制器调用：同步 HTTP、持久 Job、无 Job Poll、MCP。宿主保留固定传输、授权、原始参数及回执证据，隔离进程解释协议。原 `Runtime.Call/invoke/poll` 和输出解释代码已删除，不维护两套业务执行路径。

MCP 每页目录和每次调用绑定原账号、会话、请求者、来源版本、遗忘 epoch、包/配置版本及完整 MCP 声明摘要；管理端使用独立登录会话证明。传输原样保留工具参数数字和响应，插件投影命名空间、allowlist 与内容，宿主逐项核验。36KiB 文本及四个媒体引用不再被原 16KiB/三项处理静默截断。传输真实单帧上限仍为 1MiB，超限明确失败；不支持的资源内容类型保留私有原响应并明确报错。

无 Job 的旧轮询改为持久本地 operation：只创建一次，保存最初真实响应引用，后续每轮用该响应的完整 result.* 字段与原参数渲染原接口。没有伪造 provider ID、内部睡眠循环或重提。二进制即时完成结果通过私有引用交付。已收 HTTP 但解释/校验失败时保留证据待检查，不自动补造继续位置。

## 手机控制

`mobile-control` 0.5.0 由同一隔离 main.py 执行 native.py/bridge.py 的高层逻辑。`wechat/device_io` 只携带当前能力、来源版本、设备别名和固定步骤；插件不能提供 argv、shell、真实 serial 或宿主路径。实际 ADB 参数由宿主根据原请求及核验 profile 推导，插件没有网络、registry 或 ADB 文件挂载权限。

原 actor/source、跨账号物理租约、固定 ADB server 和 device-ledger.sqlite 继续权威。每条 I/O 前后重新检查授权，动作先记录 unknown，再确认结果；未知不重放。Windows 子进程先加入 Job 再恢复执行，清理不确定时保留锁及 cleanup owner，确认后释放。第二 native MCP 进程、临时 RuntimeRegistration 及 Manager 的专用登记 map 已删除。

PNG/MP4 留宿主私有文件，以 SHA-256 引用交接；缓存和完成结果重验文件与来源。素材页保留请求的 offset/bytes，遇到真实资源边界返回安全错误和小页续读提示。旧 650KiB 截图预算不再适用，控制器使用明确的 128MiB 内存预算并在 base64 分配前检查长度。

## 定向证据

- MCP 实际 installed Python/LPAC→宿主 HTTP：1.16 秒，覆盖分页、allowlist、uint64、36KiB 全文与同键单次提交。宿主源/忘记/伪造/媒体及旧音频权限回归通过。
- 无 Job Poll 实际 LPAC：1.78 秒，覆盖首次响应字段冻结、无 provider ID 完成和 25MiB 私有二进制恢复；取消、重启、初始引用篡改及凭据变更合成测试通过。
- 官方 mobile 单进程 LPAC→真实 OpenIO→固定合成 Runner：19.193 秒的 admin 组合通过，包含约4MiB真 PNG、动作幂等、录屏分页与原生读期间撤销来源。最终包额外 LPAC 隔离/取消测试通过，4.013 秒；devicebroker 全包含隔离测试通过，14.634 秒。
- generic MCP Raw Request 六组 race 通过，保留原 RPC 和数字、单页 cursor、allowlist、取消与实际 1MiB 边界。
- Python 外部协议20项、手机协议16项通过；迁移预演和合成浏览器已覆盖当前四项外部能力。相关 vet 和源码差异检查通过。

整合后的 `go test ./... -count=1 -timeout 600s` 已完整通过、零失败，admin 包耗时491.893秒；`go vet ./...` 通过。Windows/Linux amd64、CGO_ENABLED=0 的全部七个 cmd 构建通过；JS 静态检查通过。暂存索引、包摘要和私有值检查在提交前执行，不通过则不提交。

上述证据来自合成硬件，不能作为真实手机操作、真实服务语义、模型判断或手机播放验收。最终整体轻主体还需配置、队列、声线训练等剩余迁移与生产更新。本轮不替换生产程序、不移动模型或聊天数据。
