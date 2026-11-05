# 外部持久任务与强制隔离 MCP 流

日期：2026-11-01。仅安全合成账号、回环 HTTP、固定测试程序与项目内 Python；无付费模型调用、真实微信发送或真实设备操作，未替换生产 EXE。

## 已接通

- `external-tools` 0.4.0 新增实际 `external.job_request`。创建、单轮查询与取消均通过安装控制器、固定宿主 HTTP 和原始参数/票据摘要执行。Python 负责编号、状态和输出解释，主体删除旧重复 `Runtime.StartJob/PollJob/CancelJob` 编排。
- 原任务、请求者、来源、声线要求和旧配置哈希持久保存。轮次幂等、先 claim 后 POST、未知不重提、本地取消优先、晚到查询不能复活任务与重复投递保持有效。新建任务不借用后来发言者；会话可见的任务列表不输出私有 Origin，持久恢复资料不删除。
- 已有真实远端 ID 在状态缺失或完成输出不合格时保留，后续只查询同 ID。旧任务缺来源/配置证明时保留全部原 ID/Ticket/结果并进入待核对，不推断旧凭据。
- `Executor.StartStream` 与普通插件启动共用完整校验与强制沙箱。MCP 两个 stdio 启动路径均移除直接 exec 和宿主环境继承；旧 OS 进程辅助代码只留在测试文件。
- 管理端登记通过已安装 `plugin_id` 选择固定包，私有目录按账号与请求者 scope 分区。每次请求、发现缓存与空闲监视检查登记/配置/关闭状态，撤权后关闭旧流；不自动重绑新版本。
- 登记来源同时核对当前 MCP 配置与旧插件账本，重复 ID 冲突会拒绝；即使解析后的连接相同，声明版本替换也使旧绑定失效。

## 当前证据

| 范围 | 验证与边界 |
| --- | --- |
| 任务链路 | Job、同步 HTTP、原重启/未知/取消竞态/身份与交付回归通过；最终已知 ID 缺输出、缺状态/null 和声线续办小组通过。实际 Windows LPAC 的 start→poll→完成结果→原任务取消通过。均使用合成 HTTP。 |
| 旧 HTTP 安全边界 | bridge 实际请求验证 create 一次、重放拒绝、Ticket 持久往返、原 ID 查询、原 URL 取消、声明变化拒绝与跨源不转发创建凭据。 |
| MCP 流 | 实际 Windows LPAC scoped/unscoped discovery 与 echo、未挂载文件拒绝、环境不继承和关闭确认通过。admin 登记的默认字段、省略 transport/timeout、scope 目录隔离、停用/配置/JSON 撤销、真实 Manager/Controller 关闭空闲流均通过。 |
| 模块测试 | mcptools、pluginruntime、plugins、pluginhost 的完整包测试通过；admin 外部/插件/MCP 组合定向组通过（31.866 秒）。相关 race、vet 通过；后续小改只重验相应范围。 |
| 迁移与 UI | Go/Python 支持同步与完整 Job+Poll，空配置实际应用后停用；MCP-only 和无 Job 的 Poll 阻断。Python 预演 28 项通过，合成迁移界面与 JS 检查通过。 |

## 尚未交付的范围

完整外部 MCP 的 actor/source 代理、旧无 Job 的 Poll、配置权威与统一界面仍待迁出。需要 ADB 的旧手机 native bridge 不能套用普通离线 stdio 流：它还依赖具体 SDK、ADB 操作与授权，必须接到已核验的后端登记或宿主受控操作。不能恢复 raw exec、假用另一个入口或扩大网络授权来让测试变绿。

本轮据此增加宿主专用的固定 broker 登记通路：缺登记在 ADB server 获取前拒绝，设备能力状态明确缺少后端。登记合成包已实际通过 broker Open/Call/Close、LPAC 秘密隔离、未挂载文件拒绝、取消退出和租约释放。这个测试包不是生产 native SDK；真实设备适配仍未完成，规划能力与原始资料保持独立。

修复后的 devicebroker 与 mobile 完整模块测试通过，相关 admin 设备/规划/宿主动作目标通过；没有用未隔离 launcher 恢复原测试。Windows/Linux amd64 全部七个 cmd 的无 CGO 本地构建通过，JS 检查和外部协议 16 项通过。提交前执行暂存索引、包摘要与私有值检查。

本批未完成最终整库回归或生产配置迁移；已有流式隔离证据不能证明真实手机、GPU 或模型能力已通过。整体轻主体目标仍未完成。
